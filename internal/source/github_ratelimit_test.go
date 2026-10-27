package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stubGitHubClock fixes the time and records the waits instead of waiting.
func stubGitHubClock(t *testing.T) (now time.Time, waits *[]time.Duration) {
	t.Helper()
	now = time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	var ws []time.Duration
	prevSleep, prevNow := githubSleep, githubNow
	githubSleep = func(_ context.Context, d time.Duration) error { ws = append(ws, d); return nil }
	githubNow = func() time.Time { return now }
	t.Cleanup(func() { githubSleep, githubNow = prevSleep, prevNow })
	return now, &ws
}

// A REST request refused for the hourly limit waits until it resets, one
// refused for a secondary limit as long as Retry-After says, and both are
// then tried again.
func TestGitHubRESTWaitsForTheRateLimit(t *testing.T) {
	now, waits := stubGitHubClock(t)
	refusals := []func(w http.ResponseWriter){
		func(w http.ResponseWriter) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(now.Add(20*time.Minute).Unix(), 10))
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message": "API rate limit exceeded for user ID 1."}`))
		},
		func(w http.ResponseWriter) {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message": "You have exceeded a secondary rate limit."}`))
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(refusals) > 0 {
			refuse := refusals[0]
			refusals = refusals[1:]
			refuse(w)
			return
		}
		_, _ = w.Write([]byte(`{"total_count": 7}`))
	}))
	defer srv.Close()

	var resp struct {
		TotalCount int `json:"total_count"`
	}
	g := &GitHubEvents{Client: srv.Client(), BaseURL: srv.URL}
	if err := g.getREST(context.Background(), srv.URL+"/search/issues", &resp); err != nil {
		t.Fatal(err)
	}
	if resp.TotalCount != 7 {
		t.Errorf("total_count = %d, want 7 after the waits", resp.TotalCount)
	}
	want := []time.Duration{20*time.Minute + 5*time.Second, 30 * time.Second}
	if len(*waits) != 2 || (*waits)[0] != want[0] || (*waits)[1] != want[1] {
		t.Errorf("waits = %v, want %v", *waits, want)
	}
}

// GraphQL reports a used-up limit with HTTP 200 and a RATE_LIMITED error.
func TestGitHubGraphQLWaitsForTheRateLimit(t *testing.T) {
	now, waits := stubGitHubClock(t)
	refused := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !refused {
			refused = true
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(now.Add(10*time.Minute).Unix(), 10))
			_, _ = w.Write([]byte(`{"errors": [{"type": "RATE_LIMITED", "message": "API rate limit exceeded for user ID 1."}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data": {"ok": true}}`))
	}))
	defer srv.Close()

	g := &GitHubEvents{Client: srv.Client(), BaseURL: srv.URL}
	data, err := g.doGraphQL(context.Background(), "query { ok }", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"ok": true`) {
		t.Errorf("data = %s", data)
	}
	if len(*waits) != 1 || (*waits)[0] != 10*time.Minute+5*time.Second {
		t.Errorf("waits = %v, want one of 10m5s", *waits)
	}
}

// Other refusals fail at once, and a refusal that keeps coming, or would
// take too long to wait for, fails after all.
func TestGitHubRateLimitGivesUp(t *testing.T) {
	now, waits := stubGitHubClock(t)
	for _, c := range []struct {
		name    string
		refuse  func(w http.ResponseWriter)
		waits   int
		message string
	}{
		{"not a rate limit", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message": "Resource not accessible by integration"}`))
		}, 0, "not accessible"},
		{"keeps refusing", func(w http.ResponseWriter) {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message": "rate limit"}`))
		}, githubRateLimitRetries, "rate limit"},
		{"resets too late", func(w http.ResponseWriter) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(now.Add(3*time.Hour).Unix(), 10))
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message": "API rate limit exceeded"}`))
		}, 0, "rate limit"},
	} {
		t.Run(c.name, func(t *testing.T) {
			*waits = nil
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { c.refuse(w) }))
			defer srv.Close()
			var out struct{}
			err := (&GitHubEvents{Client: srv.Client(), BaseURL: srv.URL}).getREST(context.Background(), srv.URL+"/x", &out)
			if err == nil || !strings.Contains(err.Error(), c.message) {
				t.Errorf("err = %v, want it to mention %q", err, c.message)
			}
			if len(*waits) != c.waits {
				t.Errorf("waited %d times, want %d", len(*waits), c.waits)
			}
		})
	}
}
