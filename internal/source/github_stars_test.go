package source

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

const repoInfoWithStarsResponse = `{"data":{"repository":{"name":"repo","description":"","url":"https://github.com/owner/repo","homepageUrl":null,"createdAt":"2020-01-01T00:00:00Z","pushedAt":"2025-06-10T12:00:00Z","primaryLanguage":null,"licenseInfo":null,"repositoryTopics":{"nodes":[]},"defaultBranchRef":{"name":"main"},"isArchived":false,"stargazerCount":7015}}}`

func emptyGitHubResponses() map[string]string {
	return map[string]string{
		"forks":            `{"data":{"repository":{"forks":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"issues":           `{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"pullRequests":     `{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		"repositoryTopics": repoInfoWithStarsResponse,
	}
}

// starWeek returns a star history week starting on the Sunday of June 2025.
func starWeek(sundayInJune int, days ...int) string {
	return fmt.Sprintf(`{"week":%d,"total":0,"days":[%d,%d,%d,%d,%d,%d,%d]}`,
		dateUTC(2025, 6, sundayInJune).Unix(), days[0], days[1], days[2], days[3], days[4], days[5], days[6])
}

// recordingHandler records which star history pages were requested.
func recordingHandler(responses map[string]string, pages *[]string, mu *sync.Mutex) http.Handler {
	next := graphqlHandler(responses)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contains(r.URL.Path, "stargazers/history") {
			mu.Lock()
			*pages = append(*pages, r.URL.Query().Get("page"))
			mu.Unlock()
		}
		next.ServeHTTP(w, r)
	})
}

func starsByDate(records []Record) map[string]int64 {
	got := make(map[string]int64)
	for _, r := range filterByMetric(records, "daily_stars") {
		got[r.Date] = r.Value
	}
	return got
}

// The star count is recorded as a total_stars snapshot on the end date.
func TestGitHubEventsRecordsTotalStars(t *testing.T) {
	srv := httptest.NewServer(graphqlHandler(emptyGitHubResponses()))
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

// Daily stars come from the star history endpoint, which GitHub provides for
// every repository. Weeks come newest first, and paging stops at the first
// week that starts before the window.
func TestGitHubEventsRecordsStarHistory(t *testing.T) {
	responses := emptyGitHubResponses()
	responses["starHistory:1"] = "[" + starWeek(15, 1, 0, 2, 0, 0, 0, 6) + "]"
	responses["starHistory:2"] = "[" + starWeek(8, 5, 0, 3, 0, 0, 4, 0) + "]"
	responses["starHistory:3"] = "[" + starWeek(1, 9, 9, 9, 9, 9, 9, 9) + "]"

	var pages []string
	var mu sync.Mutex
	srv := httptest.NewServer(recordingHandler(responses, &pages, &mu))
	defer srv.Close()

	g := &GitHubEvents{Client: srv.Client(), Repo: "owner/repo", BaseURL: srv.URL}
	if _, err := g.FetchEvents(context.Background(), juneFetchOptions("proj", 10, 20)); err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}

	got := starsByDate(g.Records())
	want := map[string]int64{
		// Stars in the window. 2025-06-08 and 2025-06-21 fall outside it.
		"2025-06-10": 3, "2025-06-13": 4, "2025-06-15": 1, "2025-06-17": 2,
		// Days without stars are only stored in the last week, so a star
		// that was removed again gets overwritten.
		"2025-06-14": 0, "2025-06-16": 0, "2025-06-18": 0, "2025-06-19": 0, "2025-06-20": 0,
	}
	if len(got) != len(want) {
		t.Errorf("got daily_stars %v, want %v", got, want)
	}
	for date, n := range want {
		if v, ok := got[date]; !ok || v != n {
			t.Errorf("daily_stars on %s = %d (stored: %v), want %d", date, v, ok, n)
		}
	}
	for _, r := range filterByMetric(g.Records(), "daily_stars") {
		if r.ProjectID != "proj" || r.Target != "owner/repo" {
			t.Errorf("record %+v, want project proj and target owner/repo", r)
		}
	}
	if len(pages) != 2 {
		t.Errorf("requested star history pages %v, want [1 2]", pages)
	}
}

// A nightly run starts after the last stored day, but the last days are read
// again: GitHub's days end in US Pacific time, so the latest one may still have
// been in progress during the previous run.
func TestGitHubEventsStarHistoryRereadsLastWeek(t *testing.T) {
	responses := emptyGitHubResponses()
	responses["starHistory:1"] = "[" + starWeek(15, 1, 0, 2, 0, 0, 7, 0) + "," + starWeek(8, 5, 0, 0, 0, 0, 0, 0) + "]"

	srv := httptest.NewServer(graphqlHandler(responses))
	defer srv.Close()

	g := &GitHubEvents{Client: srv.Client(), Repo: "owner/repo", BaseURL: srv.URL}
	if _, err := g.FetchEvents(context.Background(), juneFetchOptions("proj", 20, 20)); err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}

	got := starsByDate(g.Records())
	want := map[string]int64{
		"2025-06-15": 1, "2025-06-17": 2, "2025-06-20": 7,
		"2025-06-13": 0, "2025-06-14": 0, "2025-06-16": 0, "2025-06-18": 0, "2025-06-19": 0,
	}
	if len(got) != len(want) {
		t.Errorf("got daily_stars %v, want %v", got, want)
	}
	for date, n := range want {
		if v, ok := got[date]; !ok || v != n {
			t.Errorf("daily_stars on %s = %d (stored: %v), want %d", date, v, ok, n)
		}
	}
}
