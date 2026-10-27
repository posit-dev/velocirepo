package source

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// GitHub refuses a request when a rate limit is reached: its primary limits
// per hour (REST, GraphQL), the search API's per minute, and secondary limits
// on bursts. Rather than fail the target, whose next fetch would then start
// over, a request waits for the limit to reset and tries again.

// githubRateLimitWaitMax is the longest a request waits: the primary limits
// reset within an hour.
var githubRateLimitWaitMax = 65 * time.Minute

// githubRateLimitRetries is how often a refused request is tried again.
const githubRateLimitRetries = 3

// githubRateLimitDefaultWait is the wait when GitHub doesn't say when to try
// again: the search API's limit is per minute.
var githubRateLimitDefaultWait = time.Minute + 5*time.Second

// githubSleep waits, honouring the context; tests replace it.
var githubSleep = func(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// githubNow is the time; tests replace it.
var githubNow = time.Now

// withGitHubRateLimit makes a GitHub request with do, and when GitHub refuses
// it for a rate limit, waits until the limit resets and tries again.
func withGitHubRateLimit(ctx context.Context, request string, do func() error) error {
	for attempt := 0; ; attempt++ {
		err := do()
		wait, limited := githubRateLimitWait(err, githubNow())
		if !limited || attempt == githubRateLimitRetries {
			return err
		}
		if wait > githubRateLimitWaitMax {
			return err
		}
		slog.Warn("GitHub's rate limit was reached; waiting for it to reset", "request", request, "wait", wait.Round(time.Second))
		if err := githubSleep(ctx, wait); err != nil {
			return err
		}
	}
}

// githubRateLimitWait reports whether err is GitHub refusing a request for a
// rate limit, and how long to wait before trying again: Retry-After for a
// secondary limit, until X-RateLimit-Reset for a primary one.
func githubRateLimitWait(err error, now time.Time) (time.Duration, bool) {
	var header http.Header
	var status *HTTPStatusError
	var gql *graphQLError
	switch {
	case errors.As(err, &status):
		if status.Code != http.StatusForbidden && status.Code != http.StatusTooManyRequests {
			return 0, false
		}
		header = status.Header
		if !strings.Contains(strings.ToLower(status.Message), "rate limit") &&
			header.Get("Retry-After") == "" && header.Get("X-RateLimit-Remaining") != "0" {
			return 0, false
		}
	case errors.As(err, &gql):
		// GraphQL reports a used-up primary limit with HTTP 200.
		limited := false
		for _, e := range gql.errors {
			if e.Type == "RATE_LIMITED" {
				limited = true
			}
		}
		if !limited {
			return 0, false
		}
		header = gql.header
	default:
		return 0, false
	}

	if s, err := strconv.Atoi(header.Get("Retry-After")); err == nil {
		return time.Duration(s) * time.Second, true
	}
	if header.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			// A few seconds' margin for the clocks.
			return max(time.Unix(reset, 0).Sub(now)+5*time.Second, time.Second), true
		}
	}
	return githubRateLimitDefaultWait, true
}
