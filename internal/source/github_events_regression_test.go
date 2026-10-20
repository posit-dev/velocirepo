package source

// Regression test for the GitHub finding in
// docs/review/2026-10-correctness-review.md. Fails at deabcc6.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The daily schedule fetches yesterday only, so almost every issue close and
// PR merge belongs to an item that was opened before the fetch window. Those
// must still produce issue_close / pr_merge events and refresh the item's
// content entry. At deabcc6 the issues and pullRequests loops are ordered by
// CREATED_AT and stop at the first node created before StartDate, so such
// items are never examined: only items opened and closed inside the same
// window are counted.
func TestGitHubEventsRecordsCloseAndMergeOfItemsOpenedEarlier(t *testing.T) {
	srv := httptest.NewServer(graphqlHandler(map[string]string{
		"stargazers": `{"data":{"repository":{"stargazers":{"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"forks":      `{"data":{"repository":{"forks":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"issues": `{"data":{"repository":{"issues":{"nodes":[
			{"number":5,"title":"Old bug","body":"","state":"CLOSED","createdAt":"2025-01-10T10:00:00Z","closedAt":"2025-06-10T09:00:00Z","url":"https://github.com/owner/repo/issues/5","author":{"login":"alice"},"labels":{"nodes":[]}}
		],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"pullRequests": `{"data":{"repository":{"pullRequests":{"nodes":[
			{"number":7,"title":"Older PR","body":"","state":"MERGED","createdAt":"2025-06-08T10:00:00Z","closedAt":"2025-06-10T12:00:00Z","mergedAt":"2025-06-10T12:00:00Z","url":"https://github.com/owner/repo/pull/7","author":{"login":"bob"},"labels":{"nodes":[]}}
		],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"repositoryTopics": repoInfoResponse,
	}))
	defer srv.Close()

	g := &GitHubEvents{Client: srv.Client(), Token: "test-token", Repo: "owner/repo", BaseURL: srv.URL}
	events, err := g.FetchEvents(context.Background(), juneFetchOptions("proj", 10, 10))
	if err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}

	got := map[string]int{}
	for _, e := range events {
		got[e.Type]++
	}
	if got["issue_close"] != 1 || got["pr_merge"] != 1 {
		t.Errorf("events by type = %v, want one issue_close (#5, opened 2025-01-10) and one pr_merge (#7, opened 2025-06-08)", got)
	}
	if got["issue_open"] != 0 || got["pr_open"] != 0 {
		t.Errorf("events by type = %v, want no open events for items opened before the window", got)
	}

	var issueState any
	for _, entry := range g.ContentByFilename()["issues.jsonl"] {
		if entry.ID == "issue/5" {
			issueState = entry.Extra["state"]
		}
	}
	if issueState != "CLOSED" {
		t.Errorf("content state of issue #5 = %v, want CLOSED (the content entry is never refreshed)", issueState)
	}
}

// Items are listed by last update, so an item updated after the window (today)
// is still examined, and one only commented on in the window gets its content
// refreshed without new open or close events.
func TestGitHubEventsWindowEdges(t *testing.T) {
	srv := httptest.NewServer(graphqlHandler(map[string]string{
		"stargazers": `{"data":{"repository":{"stargazers":{"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"forks":      `{"data":{"repository":{"forks":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"issues": `{"data":{"repository":{"issues":{"nodes":[
			{"number":9,"title":"Closed in the window, edited since","body":"","state":"CLOSED","createdAt":"2025-05-01T10:00:00Z","updatedAt":"2025-06-11T08:00:00Z","closedAt":"2025-06-10T09:00:00Z","url":"u","author":{"login":"alice"},"labels":{"nodes":[]}},
			{"number":8,"title":"Commented on in the window","body":"","state":"OPEN","createdAt":"2025-04-01T10:00:00Z","updatedAt":"2025-06-10T07:00:00Z","closedAt":null,"url":"u","author":{"login":"bob"},"labels":{"nodes":[]}},
			{"number":7,"title":"Untouched","body":"","state":"OPEN","createdAt":"2025-03-01T10:00:00Z","updatedAt":"2025-06-01T07:00:00Z","closedAt":null,"url":"u","author":{"login":"carol"},"labels":{"nodes":[]}}
		],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}}`,
		"pullRequests":     `{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"repositoryTopics": repoInfoResponse,
	}))
	defer srv.Close()

	g := &GitHubEvents{Client: srv.Client(), Token: "test-token", Repo: "owner/repo", BaseURL: srv.URL}
	events, err := g.FetchEvents(context.Background(), juneFetchOptions("proj", 10, 10))
	if err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}
	if len(events) != 1 || events[0].Type != "issue_close" || *events[0].Ref != 9 {
		t.Fatalf("events = %+v, want only the close of #9", events)
	}
	var ids []string
	for _, e := range g.ContentByFilename()["issues.jsonl"] {
		ids = append(ids, e.ID)
	}
	if len(ids) != 2 || ids[0] != "issue/9" || ids[1] != "issue/8" {
		t.Errorf("refreshed content = %v, want issue/9 and issue/8 (the listing stops at #7)", ids)
	}
}

// GitHub returns NOT_FOUND errors next to the data of the other items when an
// item was deleted or transferred after it was listed. The other counts are
// kept; any other error still fails the fetch.
func TestGitHubEventsReactionBatchToleratesMissingItems(t *testing.T) {
	issues := `{"data":{"repository":{"issues":{"nodes":[
		{"number":1,"title":"a","body":"","state":"OPEN","createdAt":"2025-06-10T08:00:00Z","updatedAt":"2025-06-10T08:00:00Z","closedAt":null,"url":"u","author":{"login":"alice"},"labels":{"nodes":[]}},
		{"number":2,"title":"b","body":"","state":"OPEN","createdAt":"2025-06-10T07:00:00Z","updatedAt":"2025-06-10T07:00:00Z","closedAt":null,"url":"u","author":{"login":"bob"},"labels":{"nodes":[]}}
	],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`
	run := func(reactions string) (*GitHubEvents, error) {
		srv := httptest.NewServer(graphqlHandler(map[string]string{
			"stargazers":       `{"data":{"repository":{"stargazers":{"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
			"forks":            `{"data":{"repository":{"forks":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
			"issues":           issues,
			"pullRequests":     `{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
			"repositoryTopics": repoInfoResponse,
			"reactionCounts":   reactions,
		}))
		t.Cleanup(srv.Close)
		g := &GitHubEvents{Client: srv.Client(), Token: "test-token", Repo: "owner/repo", BaseURL: srv.URL}
		_, err := g.FetchEvents(context.Background(), juneFetchOptions("proj", 10, 10))
		return g, err
	}

	g, err := run(`{"data":{"repository":{"r0":{"reactions":{"totalCount":3},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}},"r1":null}},
		"errors":[{"type":"NOT_FOUND","path":["repository","r1"],"message":"Could not resolve to an issue or pull request with the number of 2."}]}`)
	if err != nil {
		t.Fatalf("FetchEvents failed on a NOT_FOUND item: %v", err)
	}
	if records := filterByMetric(g.Records(), "total_reactions"); len(records) != 1 || records[0].Extra["ref"] != "1" || records[0].Value != 3 {
		t.Errorf("records = %+v, want only #1 with 3 reactions", records)
	}

	if _, err := run(`{"data":{"repository":{"r0":null,"r1":null}},"errors":[{"type":"FORBIDDEN","message":"Resource not accessible"}]}`); err == nil {
		t.Error("FetchEvents succeeded despite a FORBIDDEN error")
	}
}

// Comments are listed by last update, so old comments that were edited in the
// window are listed too. Only comments created in the window are events.
func TestGitHubEventsSkipsCommentsCreatedBeforeTheWindow(t *testing.T) {
	var commentPages int
	base := graphqlHandler(map[string]string{
		"stargazers":       `{"data":{"repository":{"stargazers":{"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"forks":            `{"data":{"repository":{"forks":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"issues":           `{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"pullRequests":     `{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"repositoryTopics": repoInfoResponse,
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !contains(r.URL.Path, "comments") {
			base.ServeHTTP(w, r)
			return
		}
		commentPages++
		if commentPages > 2 {
			_, _ = w.Write([]byte("[]"))
			return
		}
		var page []string
		for i := 0; i < 100; i++ {
			created := "2025-06-10T12:00:00Z"
			if commentPages > 1 || i >= 40 {
				created = "2025-05-01T12:00:00Z" // edited recently, created long ago
			}
			page = append(page, fmt.Sprintf(`{"id":%d,"created_at":%q,"issue_url":"https://api.github.com/repos/owner/repo/issues/%d","user":{"login":"u"}}`, commentPages*100+i, created, i))
		}
		_, _ = w.Write([]byte("[" + strings.Join(page, ",") + "]"))
	}))
	defer srv.Close()

	g := &GitHubEvents{Client: srv.Client(), Token: "test-token", Repo: "owner/repo", BaseURL: srv.URL}
	events, err := g.FetchEvents(context.Background(), juneFetchOptions("proj", 10, 10))
	if err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}
	if len(events) != 40 {
		t.Errorf("got %d comment events, want 40", len(events))
	}
}

// GitHub serves at most 300 pages of a repository's issue comments. Larger
// backfills continue from the last update seen, skipping the comments that
// were already read.
func TestGitHubEventsCommentsContinuePastThePageLimit(t *testing.T) {
	previous := commentPageLimit
	commentPageLimit = 2
	t.Cleanup(func() { commentPageLimit = previous })

	type comment struct {
		id      int
		updated time.Time
	}
	base := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
	var comments []comment
	for id := 1; id <= 450; id++ {
		updated := base.Add(time.Duration(id) * time.Second)
		if id == 201 {
			// The first comment past the page limit shares its update time
			// with the last one that was read.
			updated = base.Add(200 * time.Second)
		}
		comments = append(comments, comment{id, updated})
	}

	graphql := graphqlHandler(map[string]string{
		"forks":            `{"data":{"repository":{"forks":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"issues":           `{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"pullRequests":     `{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"repositoryTopics": repoInfoResponse,
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !contains(r.URL.Path, "issues/comments") {
			graphql.ServeHTTP(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("sort") != "updated" || q.Get("direction") != "asc" {
			t.Errorf("comments requested with sort=%s direction=%s, want updated asc", q.Get("sort"), q.Get("direction"))
		}
		page, _ := strconv.Atoi(q.Get("page"))
		if page > commentPageLimit {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"In order to keep the API fast for everyone, pagination is limited for this resource."}`))
			return
		}
		since, _ := time.Parse(time.RFC3339, q.Get("since"))
		var matching []string
		for _, c := range comments {
			if c.updated.Before(since) {
				continue
			}
			ts := c.updated.Format(time.RFC3339)
			matching = append(matching, fmt.Sprintf(`{"id":%d,"created_at":%q,"updated_at":%q,"issue_url":"https://api.github.com/repos/owner/repo/issues/%d","user":{"login":"u"}}`, c.id, ts, ts, c.id))
		}
		from, to := (page-1)*100, page*100
		if from > len(matching) {
			from = len(matching)
		}
		if to > len(matching) {
			to = len(matching)
		}
		_, _ = w.Write([]byte("[" + strings.Join(matching[from:to], ",") + "]"))
	}))
	defer srv.Close()

	g := &GitHubEvents{Client: srv.Client(), Token: "test-token", Repo: "owner/repo", BaseURL: srv.URL}
	events, err := g.FetchEvents(context.Background(), juneFetchOptions("proj", 10, 10))
	if err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}

	seen := make(map[int]int)
	for _, e := range events {
		if e.Type == "comment" && e.Ref != nil {
			seen[*e.Ref]++
		}
	}
	for _, c := range comments {
		if seen[c.id] != 1 {
			t.Errorf("comment %d recorded %d times, want 1", c.id, seen[c.id])
		}
	}
}
