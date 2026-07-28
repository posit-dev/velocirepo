package source

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

const repoInfoResponse = `{"data":{"repository":{"name":"repo","description":"A test repo","url":"https://github.com/owner/repo","homepageUrl":null,"createdAt":"2020-01-01T00:00:00Z","pushedAt":"2025-06-10T12:00:00Z","primaryLanguage":{"name":"Go"},"licenseInfo":{"spdxId":"MIT"},"repositoryTopics":{"nodes":[]},"defaultBranchRef":{"name":"main"},"isArchived":false}}}`

const emptyReactionCountResponse = `{"data":{"repository":{}}}`

func graphqlHandler(responses map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && contains(r.URL.Path, "comments") {
			_, _ = w.Write([]byte(`[]`))
			return
		}

		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string                 `json:"query"`
			Variables map[string]interface{} `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)

		// Handle batched reaction count queries (contain "r0:" alias pattern)
		if contains(req.Query, "r0:") {
			if resp, ok := responses["reactionCounts"]; ok {
				_, _ = w.Write([]byte(resp))
			} else {
				_, _ = w.Write([]byte(emptyReactionCountResponse))
			}
			return
		}

		for key, resp := range responses {
			if contains(req.Query, key) {
				_, _ = w.Write([]byte(resp))
				return
			}
		}
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestGitHubEventsFetchStargazers(t *testing.T) {
	resp := `{"data":{"repository":{"stargazers":{"edges":[
		{"starredAt":"2025-06-10T10:00:00Z","node":{"login":"alice"}},
		{"starredAt":"2025-06-10T11:00:00Z","node":{"login":"bob"}}
	],"pageInfo":{"hasNextPage":false,"endCursor":"c1"}}}}}`

	srv := httptest.NewServer(graphqlHandler(map[string]string{
		"stargazers":     resp,
		"forks":          `{"data":{"repository":{"forks":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"issues":         `{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"pullRequests":   `{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"repositoryTopics": repoInfoResponse,
	}))
	defer srv.Close()

	g := &GitHubEvents{
		Client:  srv.Client(),
		Token:   "test-token",
		Repo:    "owner/repo",
		BaseURL: srv.URL,
	}

	events, err := g.FetchEvents(context.Background(), juneFetchOptions("my-project", 10, 10))
	if err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Type != "star" || events[0].User != "alice" {
		t.Errorf("events[0] = %+v, want star/alice", events[0])
	}
	if events[1].Type != "star" || events[1].User != "bob" {
		t.Errorf("events[1] = %+v, want star/bob", events[1])
	}
}

func TestGitHubEventsFetchAllTypes(t *testing.T) {
	responses := map[string]string{
		"stargazers": `{"data":{"repository":{"stargazers":{"edges":[
			{"starredAt":"2025-06-10T10:00:00Z","node":{"login":"alice"}}
		],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"forks": `{"data":{"repository":{"forks":{"nodes":[
			{"createdAt":"2025-06-10T11:00:00Z","owner":{"login":"bob"}}
		],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"issues": `{"data":{"repository":{"issues":{"nodes":[
			{"number":42,"title":"Fix bug","createdAt":"2025-06-10T12:00:00Z","closedAt":"2025-06-10T14:00:00Z","author":{"login":"carol"},"labels":{"nodes":[]},"body":"","state":"CLOSED","url":"https://github.com/owner/repo/issues/42"}
		],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"pullRequests": `{"data":{"repository":{"pullRequests":{"nodes":[
			{"number":99,"title":"Add feature","createdAt":"2025-06-10T13:00:00Z","closedAt":"2025-06-10T15:00:00Z","mergedAt":"2025-06-10T15:00:00Z","author":{"login":"dave"},"labels":{"nodes":[]},"body":"","state":"MERGED","url":"https://github.com/owner/repo/pull/99"}
		],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"repositoryTopics": repoInfoResponse,
		"reactionCounts": `{"data":{"repository":{"r0":{"reactions":{"totalCount":2},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}},"r1":{"reactions":{"totalCount":0},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}}`,
	}

	srv := httptest.NewServer(graphqlHandler(responses))
	defer srv.Close()

	g := &GitHubEvents{
		Client:  srv.Client(),
		Token:   "test-token",
		Repo:    "owner/repo",
		BaseURL: srv.URL,
	}

	events, err := g.FetchEvents(context.Background(), juneFetchOptions("my-project", 10, 10))
	if err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}

	expected := []struct {
		eventType string
		user      string
		ref       *int
	}{
		{"star", "alice", nil},
		{"fork", "bob", nil},
		{"issue_open", "carol", intPtr(42)},
		{"issue_close", "carol", intPtr(42)},
		{"pr_open", "dave", intPtr(99)},
		{"pr_merge", "dave", intPtr(99)},
	}

	if len(events) != len(expected) {
		t.Fatalf("got %d events, want %d", len(events), len(expected))
	}

	for i, want := range expected {
		if events[i].Type != want.eventType {
			t.Errorf("events[%d].Type = %q, want %q", i, events[i].Type, want.eventType)
		}
		if events[i].User != want.user {
			t.Errorf("events[%d].User = %q, want %q", i, events[i].User, want.user)
		}
		if events[i].ProjectID != "my-project" {
			t.Errorf("events[%d].ProjectID = %q, want %q", i, events[i].ProjectID, "my-project")
		}
		if events[i].Target != "owner/repo" {
			t.Errorf("events[%d].GitHubRepo = %q, want %q", i, events[i].Target, "owner/repo")
		}
		if want.ref == nil {
			if events[i].Ref != nil {
				t.Errorf("events[%d].Ref = %v, want nil", i, *events[i].Ref)
			}
		} else {
			if events[i].Ref == nil || *events[i].Ref != *want.ref {
				t.Errorf("events[%d].Ref = %v, want %d", i, events[i].Ref, *want.ref)
			}
		}
	}
}

func TestGitHubEventsDateFiltering(t *testing.T) {
	responses := map[string]string{
		"stargazers": `{"data":{"repository":{"stargazers":{"edges":[
			{"starredAt":"2025-06-12T10:00:00Z","node":{"login":"alice"}},
			{"starredAt":"2025-06-10T10:00:00Z","node":{"login":"bob"}},
			{"starredAt":"2025-06-08T10:00:00Z","node":{"login":"carol"}}
		],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"forks":          `{"data":{"repository":{"forks":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"issues":         `{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"pullRequests":   `{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"repositoryTopics": repoInfoResponse,
	}

	srv := httptest.NewServer(graphqlHandler(responses))
	defer srv.Close()

	g := &GitHubEvents{
		Client:  srv.Client(),
		Repo:    "owner/repo",
		BaseURL: srv.URL,
	}

	events, err := g.FetchEvents(context.Background(), juneFetchOptions("test", 9, 11))
	if err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (only 2025-06-10 in range)", len(events))
	}
	if events[0].Datetime != "2025-06-10T10:00:00Z" {
		t.Errorf("Datetime = %q, want 2025-06-10T10:00:00Z", events[0].Datetime)
	}
}

func TestGitHubEventsPagination(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && contains(r.URL.Path, "comments") {
			_, _ = w.Write([]byte(`[]`))
			return
		}

		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string                 `json:"query"`
			Variables map[string]interface{} `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)

		if contains(req.Query, "repositoryTopics") {
			_, _ = w.Write([]byte(repoInfoResponse))
			return
		}

		if !contains(req.Query, "stargazers") {
			if contains(req.Query, "forks") {
				_, _ = w.Write([]byte(`{"data":{"repository":{"forks":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
			} else if contains(req.Query, "issues") {
				_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
			} else {
				_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
			}
			return
		}

		callCount++
		if callCount == 1 {
			_, _ = w.Write([]byte(`{"data":{"repository":{"stargazers":{"edges":[
				{"starredAt":"2025-06-10T12:00:00Z","node":{"login":"alice"}}
			],"pageInfo":{"hasNextPage":true,"endCursor":"cursor1"}}}}}`))
		} else {
			_, _ = w.Write([]byte(`{"data":{"repository":{"stargazers":{"edges":[
				{"starredAt":"2025-06-10T11:00:00Z","node":{"login":"bob"}}
			],"pageInfo":{"hasNextPage":false,"endCursor":"cursor2"}}}}}`))
		}
	}))
	defer srv.Close()

	g := &GitHubEvents{
		Client:  srv.Client(),
		Repo:    "owner/repo",
		BaseURL: srv.URL,
	}

	events, err := g.FetchEvents(context.Background(), juneFetchOptions("test", 10, 10))
	if err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].User != "alice" || events[1].User != "bob" {
		t.Errorf("unexpected users: %s, %s", events[0].User, events[1].User)
	}
}

func TestGitHubEventsInvalidRepo(t *testing.T) {
	g := &GitHubEvents{
		Client: http.DefaultClient,
		Repo:   "invalid",
	}

	_, err := g.FetchEvents(context.Background(), fetchOptions("test", time.Now(), time.Now()))
	if err == nil {
		t.Fatal("expected error for invalid repo")
	}
}

func TestGitHubEventsAuthHeader(t *testing.T) {
	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		assertBearerToken(t, r, "my-secret-token")
		if r.Method == http.MethodGet && contains(r.URL.Path, "comments") {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct{ Query string `json:"query"` }
		_ = json.Unmarshal(body, &req)
		if contains(req.Query, "repositoryTopics") {
			_, _ = w.Write([]byte(repoInfoResponse))
		} else if contains(req.Query, "stargazers") {
			_, _ = w.Write([]byte(`{"data":{"repository":{"stargazers":{"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
		} else if contains(req.Query, "forks") {
			_, _ = w.Write([]byte(`{"data":{"repository":{"forks":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
		} else if contains(req.Query, "issues") {
			_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
		} else {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
		}
	}))
	defer srv.Close()

	g := &GitHubEvents{
		Client:  srv.Client(),
		Token:   "my-secret-token",
		Repo:    "owner/repo",
		BaseURL: srv.URL,
	}

	_, err := g.FetchEvents(context.Background(), fetchOptions("test", time.Now().AddDate(0, 0, -7), time.Now()))
	if err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}
	if !called.Load() {
		t.Fatal("expected FetchEvents to make an HTTP request")
	}
}

func TestGitHubEventsPRNotMerged(t *testing.T) {
	responses := map[string]string{
		"stargazers":     `{"data":{"repository":{"stargazers":{"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"forks":          `{"data":{"repository":{"forks":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"issues":         `{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"repositoryTopics": repoInfoResponse,
		"reactionCounts": `{"data":{"repository":{"r0":{"reactions":{"totalCount":0},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}}`,
		"pullRequests": `{"data":{"repository":{"pullRequests":{"nodes":[
			{"number":7,"title":"Some PR","createdAt":"2025-06-10T10:00:00Z","closedAt":"2025-06-10T12:00:00Z","mergedAt":null,"author":{"login":"alice"},"labels":{"nodes":[]},"body":"","state":"CLOSED","url":"https://github.com/owner/repo/pull/7"}
		],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
	}

	srv := httptest.NewServer(graphqlHandler(responses))
	defer srv.Close()

	g := &GitHubEvents{
		Client:  srv.Client(),
		Repo:    "owner/repo",
		BaseURL: srv.URL,
	}

	events, err := g.FetchEvents(context.Background(), juneFetchOptions("test", 10, 10))
	if err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (only pr_open, no merge)", len(events))
	}
	if events[0].Type != "pr_open" {
		t.Errorf("EventType = %q, want pr_open", events[0].Type)
	}
}

func TestGitHubEventsIssueCloseOutOfRange(t *testing.T) {
	responses := map[string]string{
		"stargazers":     `{"data":{"repository":{"stargazers":{"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"forks":          `{"data":{"repository":{"forks":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"repositoryTopics": repoInfoResponse,
		"reactionCounts": `{"data":{"repository":{"r0":{"reactions":{"totalCount":0},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}}`,
		"issues": `{"data":{"repository":{"issues":{"nodes":[
			{"number":5,"title":"Some issue","createdAt":"2025-06-10T10:00:00Z","closedAt":"2025-06-20T10:00:00Z","author":{"login":"alice"},"labels":{"nodes":[]},"body":"","state":"CLOSED","url":"https://github.com/owner/repo/issues/5"}
		],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"pullRequests":   `{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
	}

	srv := httptest.NewServer(graphqlHandler(responses))
	defer srv.Close()

	g := &GitHubEvents{
		Client:  srv.Client(),
		Repo:    "owner/repo",
		BaseURL: srv.URL,
	}

	events, err := g.FetchEvents(context.Background(), juneFetchOptions("test", 10, 11))
	if err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (issue_open only, close is out of range)", len(events))
	}
	if events[0].Type != "issue_open" {
		t.Errorf("EventType = %q, want issue_open", events[0].Type)
	}
}

func TestGitHubEventsGraphQLError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"Bad credentials"}]}`))
	}))
	defer srv.Close()

	g := &GitHubEvents{
		Client:  srv.Client(),
		Repo:    "owner/repo",
		BaseURL: srv.URL,
	}

	_, err := g.FetchEvents(context.Background(), fetchOptions("test", time.Now(), time.Now()))
	if err == nil {
		t.Fatal("expected error for GraphQL error response")
	}
}

func TestGitHubEventsContent(t *testing.T) {
	responses := map[string]string{
		"stargazers": `{"data":{"repository":{"stargazers":{"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"forks":      `{"data":{"repository":{"forks":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"issues": `{"data":{"repository":{"issues":{"nodes":[
			{"number":10,"title":"Bug report","body":"Something is broken","state":"OPEN","createdAt":"2025-06-10T10:00:00Z","closedAt":null,"url":"https://github.com/owner/repo/issues/10","author":{"login":"alice"},"labels":{"nodes":[{"name":"bug"},{"name":"urgent"}]}}
		],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"pullRequests": `{"data":{"repository":{"pullRequests":{"nodes":[
			{"number":20,"title":"Fix bug","body":"This fixes #10","state":"MERGED","createdAt":"2025-06-10T11:00:00Z","closedAt":"2025-06-10T12:00:00Z","mergedAt":"2025-06-10T12:00:00Z","url":"https://github.com/owner/repo/pull/20","author":{"login":"bob"},"labels":{"nodes":[{"name":"fix"}]}}
		],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"repositoryTopics": repoInfoResponse,
		"reactionCounts": `{"data":{"repository":{"r0":{"reactions":{"totalCount":5},"comments":{"nodes":[{"reactions":{"totalCount":2}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}},"r1":{"reactions":{"totalCount":0},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}}`,
	}

	srv := httptest.NewServer(graphqlHandler(responses))
	defer srv.Close()

	g := &GitHubEvents{
		Client:  srv.Client(),
		Repo:    "owner/repo",
		BaseURL: srv.URL,
	}

	_, err := g.FetchEvents(context.Background(), juneFetchOptions("my-project", 10, 10))
	if err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}

	content := g.ContentByFilename()
	if len(content) != 3 {
		t.Fatalf("got %d content files, want 3 (issues, prs, repos)", len(content))
	}

	issues := content["issues.jsonl"]
	if len(issues) != 1 {
		t.Fatalf("got %d issue entries, want 1", len(issues))
	}
	if issues[0].ID != "issue/10" {
		t.Errorf("issue ID = %q, want issue/10", issues[0].ID)
	}
	if issues[0].Ref == nil || *issues[0].Ref != 10 {
		t.Errorf("issue Ref = %v, want 10", issues[0].Ref)
	}
	if issues[0].Title != "Bug report" {
		t.Errorf("issue Title = %q, want Bug report", issues[0].Title)
	}
	if len(issues[0].Tags) != 2 || issues[0].Tags[0] != "bug" {
		t.Errorf("issue Tags = %v, want [bug urgent]", issues[0].Tags)
	}
	if issues[0].Type != "issue" {
		t.Errorf("issue Type = %q, want issue", issues[0].Type)
	}

	prs := content["prs.jsonl"]
	if len(prs) != 1 {
		t.Fatalf("got %d PR entries, want 1", len(prs))
	}
	if prs[0].ID != "pr/20" {
		t.Errorf("PR ID = %q, want pr/20", prs[0].ID)
	}
	if prs[0].Ref == nil || *prs[0].Ref != 20 {
		t.Errorf("PR Ref = %v, want 20", prs[0].Ref)
	}
	if prs[0].Extra["merged_at"] != "2025-06-10T12:00:00Z" {
		t.Errorf("PR merged_at = %v, want 2025-06-10T12:00:00Z", prs[0].Extra["merged_at"])
	}

	repos := content["repos.jsonl"]
	if len(repos) != 1 {
		t.Fatalf("got %d repo entries, want 1", len(repos))
	}
	if repos[0].ID != "owner/repo" {
		t.Errorf("repo ID = %q, want owner/repo", repos[0].ID)
	}
	if repos[0].Type != "repo" {
		t.Errorf("repo Type = %q, want repo", repos[0].Type)
	}
	if repos[0].Extra["language"] != "Go" {
		t.Errorf("repo language = %v, want Go", repos[0].Extra["language"])
	}
}

func TestGitHubEventsComments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && contains(r.URL.Path, "comments") {
			_, _ = w.Write([]byte(`[
				{"created_at":"2025-06-10T09:00:00Z","issue_url":"https://api.github.com/repos/owner/repo/issues/42","user":{"login":"alice"}},
				{"created_at":"2025-06-10T10:00:00Z","issue_url":"https://api.github.com/repos/owner/repo/issues/7","user":{"login":"bob"}},
				{"created_at":"2025-06-10T11:00:00Z","issue_url":"https://api.github.com/repos/owner/repo/issues/99","user":null}
			]`))
			return
		}

		body, _ := io.ReadAll(r.Body)
		var req struct{ Query string `json:"query"` }
		_ = json.Unmarshal(body, &req)
		if contains(req.Query, "repositoryTopics") {
			_, _ = w.Write([]byte(repoInfoResponse))
		} else if contains(req.Query, "stargazers") {
			_, _ = w.Write([]byte(`{"data":{"repository":{"stargazers":{"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
		} else if contains(req.Query, "forks") {
			_, _ = w.Write([]byte(`{"data":{"repository":{"forks":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
		} else if contains(req.Query, "issues") {
			_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
		} else {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
		}
	}))
	defer srv.Close()

	g := &GitHubEvents{
		Client:  srv.Client(),
		Repo:    "owner/repo",
		BaseURL: srv.URL,
	}

	events, err := g.FetchEvents(context.Background(), juneFetchOptions("test", 10, 10))
	if err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("got %d events, want 3 comments", len(events))
	}
	for _, e := range events {
		if e.Type != "comment" {
			t.Errorf("Type = %q, want comment", e.Type)
		}
	}
	if events[0].Ref == nil || *events[0].Ref != 42 {
		t.Errorf("events[0].Ref = %v, want 42", events[0].Ref)
	}
	if events[0].User != "alice" {
		t.Errorf("events[0].user = %q, want alice", events[0].User)
	}
	if events[1].Ref == nil || *events[1].Ref != 7 {
		t.Errorf("events[1].Ref = %v, want 7", events[1].Ref)
	}
	if events[2].Ref == nil || *events[2].Ref != 99 {
		t.Errorf("events[2].Ref = %v, want 99", events[2].Ref)
	}
	if events[2].User != "" {
		t.Errorf("events[2].User = %q, want empty (no user)", events[2].User)
	}
}

func TestGitHubEventsReactionCounts(t *testing.T) {
	responses := map[string]string{
		"stargazers":     `{"data":{"repository":{"stargazers":{"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"forks":          `{"data":{"repository":{"forks":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"repositoryTopics": repoInfoResponse,
		"issues": `{"data":{"repository":{"issues":{"nodes":[
			{"number":10,"title":"Bug","body":"","state":"OPEN","createdAt":"2025-06-10T09:00:00Z","closedAt":null,"url":"https://github.com/owner/repo/issues/10","author":{"login":"alice"},"labels":{"nodes":[]}}
		],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"pullRequests": `{"data":{"repository":{"pullRequests":{"nodes":[
			{"number":20,"title":"Fix","body":"","state":"MERGED","createdAt":"2025-06-10T09:30:00Z","closedAt":"2025-06-10T10:00:00Z","mergedAt":"2025-06-10T10:00:00Z","url":"https://github.com/owner/repo/pull/20","author":{"login":"bob"},"labels":{"nodes":[]}}
		],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"reactionCounts": `{"data":{"repository":{"r0":{"reactions":{"totalCount":3},"comments":{"nodes":[{"reactions":{"totalCount":2}},{"reactions":{"totalCount":1}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}},"r1":{"reactions":{"totalCount":1},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}}`,
	}

	srv := httptest.NewServer(graphqlHandler(responses))
	defer srv.Close()

	g := &GitHubEvents{
		Client:  srv.Client(),
		Repo:    "owner/repo",
		BaseURL: srv.URL,
	}

	_, err := g.FetchEvents(context.Background(), juneFetchOptions("test", 10, 10))
	if err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}

	records := g.Records()
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}

	// Issue #10: 3 body + 2 + 1 comment reactions = 6
	if records[0].Metric != "total_reactions" {
		t.Errorf("records[0].Metric = %q, want total_reactions", records[0].Metric)
	}
	if records[0].Value != 6 {
		t.Errorf("records[0].Value = %d, want 6 (3 body + 2 + 1 comment)", records[0].Value)
	}
	if records[0].Extra["ref"] != "10" {
		t.Errorf("records[0].Extra[ref] = %q, want 10", records[0].Extra["ref"])
	}

	// PR #20: 1 body + 0 comment reactions = 1
	if records[1].Value != 1 {
		t.Errorf("records[1].Value = %d, want 1", records[1].Value)
	}
	if records[1].Extra["ref"] != "20" {
		t.Errorf("records[1].Extra[ref] = %q, want 20", records[1].Extra["ref"])
	}
}

func TestGitHubEventsReactionCountsSkipsKnownClosed(t *testing.T) {
	// Set up a data dir with an existing total_reactions record for closed issue #5
	dir := t.TempDir()
	metricsDir := dir + "/metrics/github/test"
	_ = os.MkdirAll(metricsDir, 0755)
	_ = os.WriteFile(metricsDir+"/2025-06-09.jsonl", []byte(
		`{"source":"github","metric":"total_reactions","project_id":"test","target":"owner/repo","date":"2025-06-09","value":3,"extra":{"ref":"5"}}`+"\n",
	), 0644)

	responses := map[string]string{
		"stargazers":     `{"data":{"repository":{"stargazers":{"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"forks":          `{"data":{"repository":{"forks":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"repositoryTopics": repoInfoResponse,
		"issues": `{"data":{"repository":{"issues":{"nodes":[
			{"number":5,"title":"Old issue","body":"","state":"CLOSED","createdAt":"2025-06-10T10:00:00Z","closedAt":"2025-06-10T12:00:00Z","url":"https://github.com/owner/repo/issues/5","author":{"login":"alice"},"labels":{"nodes":[]}}
		],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"pullRequests": `{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
	}

	srv := httptest.NewServer(graphqlHandler(responses))
	defer srv.Close()

	g := &GitHubEvents{
		Client:  srv.Client(),
		Repo:    "owner/repo",
		BaseURL: srv.URL,
	}

	opts := juneFetchOptions("test", 10, 10)
	opts.DataDir = dir
	_, err := g.FetchEvents(context.Background(), opts)
	if err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}

	// Closed issue #5 already has a record, so no new records should be produced
	records := g.Records()
	if len(records) != 0 {
		t.Fatalf("got %d records, want 0 (closed issue already has record)", len(records))
	}
}

func intPtr(n int) *int { return &n }
