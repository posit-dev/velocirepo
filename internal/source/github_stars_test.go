package source

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

const repoInfoWithStarsResponse = `{"data":{"repository":{"name":"repo","description":"","url":"https://github.com/owner/repo","homepageUrl":null,"createdAt":"2020-01-01T00:00:00Z","pushedAt":"2025-06-10T12:00:00Z","primaryLanguage":null,"licenseInfo":null,"repositoryTopics":{"nodes":[]},"defaultBranchRef":{"name":"main"},"isArchived":false,"stargazerCount":7015}}}`

func emptyGitHubResponses(stargazers string) map[string]string {
	return map[string]string{
		"stargazers":       stargazers,
		"forks":            `{"data":{"repository":{"forks":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"issues":           `{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"pullRequests":     `{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"repositoryTopics": repoInfoWithStarsResponse,
	}
}

// The star count is recorded as a total_stars snapshot on the end date, so
// stars keep being counted when GitHub won't list a repo's stargazers.
func TestGitHubEventsRecordsTotalStars(t *testing.T) {
	srv := httptest.NewServer(graphqlHandler(emptyGitHubResponses(
		`{"data":{"repository":{"stargazerCount":7015,"stargazers":{"totalCount":7015,"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`)))
	defer srv.Close()

	g := &GitHubEvents{Client: srv.Client(), Repo: "owner/repo", BaseURL: srv.URL}
	if _, err := g.FetchEvents(context.Background(), juneFetchOptions("proj", 1, 10)); err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}

	stars := filterByMetric(g.Records(), "total_stars")
	if len(stars) != 1 {
		t.Fatalf("got %d total_stars records, want 1", len(stars))
	}
	got := stars[0]
	if got.Value != 7015 || got.Date != "2025-06-10" || got.ProjectID != "proj" || got.Target != "owner/repo" {
		t.Errorf("total_stars = %+v, want 7015 on 2025-06-10 for proj, owner/repo", got)
	}
}

// GitHub returns an empty stargazer list for repos the token can't
// administer, while stargazerCount still reports the stars. That must be
// reported instead of looking like a quiet day.
func TestGitHubEventsWarnsWhenStargazersAreHidden(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	srv := httptest.NewServer(graphqlHandler(emptyGitHubResponses(
		`{"data":{"repository":{"stargazerCount":7015,"stargazers":{"totalCount":0,"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`)))
	defer srv.Close()

	g := &GitHubEvents{Client: srv.Client(), Repo: "owner/repo", BaseURL: srv.URL}
	events, err := g.FetchEvents(context.Background(), juneFetchOptions("proj", 1, 10))
	if err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0", len(events))
	}
	if !strings.Contains(logs.String(), "stargazers") || !strings.Contains(logs.String(), "owner/repo") {
		t.Errorf("expected a warning about hidden stargazers for owner/repo, got logs:\n%s", logs.String())
	}
}

// A repo without stars has an empty list too. That's not hidden.
func TestGitHubEventsNoWarningForRepoWithoutStars(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	srv := httptest.NewServer(graphqlHandler(emptyGitHubResponses(
		`{"data":{"repository":{"stargazerCount":0,"stargazers":{"totalCount":0,"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`)))
	defer srv.Close()

	g := &GitHubEvents{Client: srv.Client(), Repo: "owner/repo", BaseURL: srv.URL}
	if _, err := g.FetchEvents(context.Background(), juneFetchOptions("proj", 1, 10)); err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}
	if logs.Len() != 0 {
		t.Errorf("expected no warnings, got:\n%s", logs.String())
	}
}
