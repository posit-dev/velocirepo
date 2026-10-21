package source

import (
	"bytes"
	"context"
	"fmt"
	"io"
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

// A repository without stored daily stars gets its whole star history, so
// upgrading doesn't need a full re-fetch of every issue and comment.
func TestGitHubEventsBackfillsStarHistory(t *testing.T) {
	responses := emptyGitHubResponses()
	responses["starHistory:1"] = "[" + starWeek(15, 1, 0, 0, 0, 0, 0, 0) + "]"
	responses["starHistory:2"] = "[" + starWeek(8, 2, 0, 0, 0, 0, 0, 0) + "]"
	responses["starHistory:3"] = "[" + starWeek(1, 3, 0, 0, 0, 0, 0, 0) + "]"

	srv := httptest.NewServer(graphqlHandler(responses))
	defer srv.Close()

	g := &GitHubEvents{Client: srv.Client(), Repo: "owner/repo", BaseURL: srv.URL}
	opts := juneFetchOptions("proj", 20, 20)
	opts.StarHistoryBackfill = map[string]bool{"owner/repo": true}
	if _, err := g.FetchEvents(context.Background(), opts); err != nil {
		t.Fatalf("FetchEvents failed: %v", err)
	}

	got := starsByDate(g.Records())
	for date, n := range map[string]int64{"2025-06-01": 3, "2025-06-08": 2, "2025-06-15": 1} {
		if got[date] != n {
			t.Errorf("daily_stars on %s = %d, want %d (got %v)", date, got[date], n, got)
		}
	}
	// Days without stars before the reread window aren't stored.
	if _, ok := got["2025-06-02"]; ok {
		t.Errorf("stored a zero for 2025-06-02: %v", got)
	}
}

// GitHub's snapshots (repository info and reaction counts) can be fetched on
// their own, for the issue and pull request numbers recorded when they were
// first fetched, so they can be replayed without the event requests, which
// aren't archived. Their requests are marked for the archive.
func TestGitHubSnapshotUnit(t *testing.T) {
	var eventRequests, archived int
	responses := emptyGitHubResponses()
	responses["reactionCounts"] = `{"data":{"repository":{"r0":{"reactions":{"totalCount":3},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}}`
	handler := graphqlHandler(responses)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if contains(string(body), "forks") || contains(string(body), "issues(") || contains(r.URL.Path, "comments") || contains(r.URL.Path, "stargazers") {
			eventRequests++
		}
		handler.ServeHTTP(w, r)
	}))
	defer srv.Close()

	client := srv.Client()
	inner := client.Transport
	if inner == nil {
		inner = http.DefaultTransport
	}
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if mode, ok := ArchiveModeOf(r.Context()); ok && mode == ArchiveFull {
			archived++
		}
		return inner.RoundTrip(r)
	})

	g := &GitHubEvents{Client: client, Repo: "owner/repo", BaseURL: srv.URL}
	if err := g.FetchSnapshots(context.Background(), juneFetchOptions("proj", 20, 20), []int{7}); err != nil {
		t.Fatalf("FetchSnapshots failed: %v", err)
	}
	if eventRequests != 0 {
		t.Errorf("the snapshot unit made %d event requests", eventRequests)
	}
	if archived != 2 {
		t.Errorf("%d snapshot requests were marked for the archive, want 2 (repository info and reaction counts)", archived)
	}
	reactions := filterByMetric(g.Records(), "total_reactions")
	if len(reactions) != 1 || reactions[0].Extra["ref"] != "7" || reactions[0].Value != 3 {
		t.Errorf("total_reactions = %+v, want 3 for #7", reactions)
	}
	if stars := filterByMetric(g.Records(), "total_stars"); len(stars) != 1 {
		t.Errorf("total_stars = %+v, want one", stars)
	}
	if got := g.SnapshotRefs(); len(got) != 1 || got[0] != 7 {
		t.Errorf("SnapshotRefs() = %v, want [7]", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
