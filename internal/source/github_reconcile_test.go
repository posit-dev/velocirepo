package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// GitHub's batched totals, with a repository that no longer exists.
func TestGitHubReferenceTotals(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req graphqlRequest
		_ = json.Unmarshal(body, &req)
		queries = append(queries, req.Query)
		_, _ = fmt.Fprint(w, `{"data": {
			"r0": {"issues": {"totalCount": 8172}, "pullRequests": {"totalCount": 3000}, "merged": {"totalCount": 2500}, "forks": {"totalCount": 120}, "stargazerCount": 3400},
			"r1": null},
			"errors": [{"type": "NOT_FOUND", "message": "Could not resolve to a Repository"}]}`)
	}))
	defer srv.Close()

	g := &GitHubEvents{Client: srv.Client(), BaseURL: srv.URL}
	totals, err := g.ReferenceTotals(context.Background(), []string{"posit-dev/positron", "posit-dev/gone"})
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 || !strings.Contains(queries[0], `r1: repository(owner: "posit-dev", name: "gone")`) {
		t.Errorf("queries = %q, want both repositories in one", queries)
	}
	got := map[string]int64{}
	for _, tot := range totals {
		if tot.Target == "posit-dev/gone" {
			if tot.Unavailable == "" {
				t.Errorf("a missing repository has totals: %+v", tot)
			}
			continue
		}
		got[tot.Metric] = tot.Value
		if tot.Tolerance < githubReferenceSlack {
			t.Errorf("%s tolerance = %d", tot.Metric, tot.Tolerance)
		}
	}
	want := map[string]int64{"issue_open": 8172, "pr_open": 3000, "pr_merge": 2500, "fork": 120, "daily_stars": 3400}
	for m, v := range want {
		if got[m] != v {
			t.Errorf("%s = %d, want %d", m, got[m], v)
		}
	}
}

func TestGitHubReferenceTotalsBatches(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = fmt.Fprint(w, `{"data": {}}`)
	}))
	defer srv.Close()
	targets := make([]string, 465)
	for i := range targets {
		targets[i] = fmt.Sprintf("org/repo%d", i)
	}
	if _, err := (&GitHubEvents{Client: srv.Client(), BaseURL: srv.URL}).ReferenceTotals(context.Background(), targets); err != nil {
		t.Fatal(err)
	}
	if requests > 15 {
		t.Errorf("465 repositories took %d requests, more than 15", requests)
	}
}

func TestGitHubMonthlyReferenceTotals(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		queries = append(queries, q)
		if strings.Contains(q, "repo:org/gone") {
			http.Error(w, `{"message":"Validation Failed"}`, http.StatusUnprocessableEntity)
			return
		}
		_, _ = fmt.Fprintf(w, `{"total_count": %d}`, len(queries))
	}))
	defer srv.Close()
	from, through := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	waits := 0
	totals, err := (&GitHubEvents{Client: srv.Client(), BaseURL: srv.URL}).MonthlyReferenceTotals(context.Background(), []string{"org/repo", "org/gone"}, from, through,
		func(context.Context) error { waits++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if queries[0] != "repo:org/repo is:issue created:2026-09-01..2026-09-30" || queries[3] != "repo:org/repo is:pr merged:2026-09-01..2026-09-30" {
		t.Errorf("queries = %q", queries)
	}
	if waits != 8 || len(totals) != 8 {
		t.Errorf("%d waits, %d totals", waits, len(totals))
	}
	if totals[0].Metric != "issue_open" || totals[0].Value != 1 || totals[4].Unavailable == "" {
		t.Errorf("totals = %+v", totals)
	}
}
