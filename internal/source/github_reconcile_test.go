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
			"r0": {"issues": {"totalCount": 8172}, "pullRequests": {"totalCount": 3000}, "merged": {"totalCount": 2500}, "forkCount": 120, "stargazerCount": 3400},
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
