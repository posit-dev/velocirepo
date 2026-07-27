package source

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const repoInfoResponse = `{"data":{"repository":{"name":"repo","description":"A test repo","url":"https://github.com/owner/repo","homepageUrl":null,"createdAt":"2020-01-01T00:00:00Z","pushedAt":"2025-06-10T12:00:00Z","primaryLanguage":{"name":"Go"},"licenseInfo":{"spdxId":"MIT"},"repositoryTopics":{"nodes":[]},"defaultBranchRef":{"name":"main"},"isArchived":false}}}`

func graphqlHandler(responses map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string                 `json:"query"`
			Variables map[string]interface{} `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)

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
	if events[0].Type != "star" || events[0].Tags["user"] != "alice" {
		t.Errorf("events[0] = %+v, want star/alice", events[0])
	}
	if events[1].Type != "star" || events[1].Tags["user"] != "bob" {
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
		if events[i].Tags["user"] != want.user {
			t.Errorf("events[%d].User = %q, want %q", i, events[i].Tags["user"], want.user)
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
	if events[0].Tags["user"] != "alice" || events[1].Tags["user"] != "bob" {
		t.Errorf("unexpected users: %s, %s", events[0].Tags["user"], events[1].Tags["user"])
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

func intPtr(n int) *int { return &n }
