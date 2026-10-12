package source

// Regression test for the GitHub finding in
// docs/review/2026-10-correctness-review.md. Fails at deabcc6.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

// Comments come newest first. Once one predates the window, the rest do too,
// so paging stops instead of walking every edited old comment.
func TestGitHubEventsCommentPagingStopsAtTheWindow(t *testing.T) {
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
			page = append(page, fmt.Sprintf(`{"created_at":%q,"issue_url":"https://api.github.com/repos/owner/repo/issues/%d","user":{"login":"u"}}`, created, i))
		}
		_, _ = w.Write([]byte("[" + strings.Join(page, ",") + "]"))
	}))
	defer srv.Close()

	g := &GitHubEvents{Client: srv.Client(), Token: "test-token", Repo: "owner/repo", BaseURL: srv.URL}
	events, err := g.FetchEvents(context.Background(), juneFetchOptions("proj", 10, 10))
	if err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}
	if commentPages != 1 {
		t.Errorf("requested %d comment pages, want 1", commentPages)
	}
	if len(events) != 40 {
		t.Errorf("got %d comment events, want 40", len(events))
	}
}
