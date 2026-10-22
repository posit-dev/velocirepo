package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// githubReferenceBatch is how many repositories one reference query asks
// for. Each costs about half a point of GitHub's GraphQL quota, so devrel-io's
// 465 repositories take 12 requests.
const githubReferenceBatch = 40

// githubReferenceTolerance is how far stored totals may differ from GitHub's
// all-time totals, relative to them, and at least githubReferenceSlack:
// deleted forks and stars taken back. Items opened since the last day fetched
// are allowed for by reconcile, from the target's recent activity.
const (
	githubReferenceTolerance = 0.01
	githubReferenceSlack     = 5
)

var _ Reconciler = (*GitHubEvents)(nil)

type githubRepoTotals struct {
	Issues         struct{ TotalCount int64 } `json:"issues"`
	PullRequests   struct{ TotalCount int64 } `json:"pullRequests"`
	Merged         struct{ TotalCount int64 } `json:"merged"`
	Forks          struct{ TotalCount int64 } `json:"forks"`
	StargazerCount int64                      `json:"stargazerCount"`
}

// ReferenceTotals returns GitHub's all-time totals of each repository's
// issues, pull requests, merged pull requests, forks and stars, many
// repositories per query. A repository that doesn't exist (anymore) gets
// unavailable totals.
func (g *GitHubEvents) ReferenceTotals(ctx context.Context, targets []string) ([]ReferenceTotal, error) {
	var out []ReferenceTotal
	for start := 0; start < len(targets); start += githubReferenceBatch {
		batch := targets[start:min(start+githubReferenceBatch, len(targets))]
		totals, err := g.referenceBatch(ctx, batch)
		if err != nil {
			return out, err
		}
		out = append(out, totals...)
	}
	return out, nil
}

func (g *GitHubEvents) referenceBatch(ctx context.Context, repos []string) ([]ReferenceTotal, error) {
	var q strings.Builder
	q.WriteString("query {")
	for i, repo := range repos {
		owner, name, ok := strings.Cut(repo, "/")
		if !ok {
			return nil, fmt.Errorf("repository %q isn't owner/name", repo)
		}
		fmt.Fprintf(&q, ` r%d: repository(owner: %q, name: %q) { issues { totalCount } pullRequests { totalCount } merged: pullRequests(states: MERGED) { totalCount } forks { totalCount } stargazerCount }`, i, owner, name)
	}
	q.WriteString(" }")

	data, err := g.doGraphQL(ctx, q.String(), nil)
	var gqlErr *graphQLError
	if errors.As(err, &gqlErr) && gqlErr.onlyNotFound() {
		data, err = gqlErr.body, nil
	}
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data map[string]*githubRepoTotals `json:"data"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decode reference totals: %w", err)
	}

	var out []ReferenceTotal
	for i, repo := range repos {
		t := resp.Data[fmt.Sprintf("r%d", i)]
		if t == nil {
			out = append(out, ReferenceTotal{Target: repo, Unavailable: "the repository doesn't exist or isn't accessible"})
			continue
		}
		for _, r := range []struct {
			kind, metric, label string
			value               int64
			mayExceed           bool
		}{
			{"events", "issue_open", "issues", t.Issues.TotalCount, false},
			{"events", "pr_open", "pull requests", t.PullRequests.TotalCount, false},
			{"events", "pr_merge", "merged pull requests", t.Merged.TotalCount, false},
			// Direct forks, as velocirepo lists them; forkCount is the
			// whole fork network. Deleted forks stay stored.
			{"events", "fork", "forks", t.Forks.TotalCount, true},
			{"records", "daily_stars", "stars", t.StargazerCount, false},
		} {
			tol := max(int64(githubReferenceSlack), int64(float64(r.value)*githubReferenceTolerance))
			out = append(out, ReferenceTotal{Target: repo, Kind: r.kind, Metric: r.metric, Label: r.label, Value: r.value, Tolerance: tol, MayExceed: r.mayExceed})
		}
	}
	return out, nil
}

// githubMonthlySearches are the per-month counts GitHub's search API gives,
// against the stored events they correspond to.
var githubMonthlySearches = []struct{ metric, label, query string }{
	{"issue_open", "issues opened", "is:issue created:%s"},
	{"issue_close", "issues closed", "is:issue closed:%s"},
	{"pr_open", "pull requests opened", "is:pr created:%s"},
	{"pr_merge", "pull requests merged", "is:pr merged:%s"},
}

var _ MonthlyReconciler = (*GitHubEvents)(nil)

// MonthlyRequests is how many searches one repository's month takes.
func (g *GitHubEvents) MonthlyRequests() int { return len(githubMonthlySearches) }

// MonthlyQuota is the search API's, which only reconciliation uses.
func (g *GitHubEvents) MonthlyQuota() string { return "api.github.com/search" }

// MonthlyReferenceTotals returns, per repository, the numbers of issues and
// pull requests opened, closed and merged from one day through another, in
// UTC, from the search API. They find gaps that all-time totals don't, such
// as a missing week that happens to balance out.
func (g *GitHubEvents) MonthlyReferenceTotals(ctx context.Context, targets []string, from, through time.Time, wait func(context.Context) error) ([]ReferenceTotal, error) {
	window := from.Format("2006-01-02") + ".." + through.Format("2006-01-02")
	var out []ReferenceTotal
	for _, repo := range targets {
		for _, s := range githubMonthlySearches {
			if wait != nil {
				if err := wait(ctx); err != nil {
					return out, err
				}
			}
			q := "repo:" + repo + " " + fmt.Sprintf(s.query, window)
			var resp struct {
				TotalCount int64 `json:"total_count"`
			}
			u := g.restBaseURL() + "/search/issues?per_page=1&q=" + url.QueryEscape(q)
			total := ReferenceTotal{Target: repo, Kind: "events", Metric: s.metric, Label: s.label, From: from, Through: through}
			if err := g.searchREST(ctx, u, &resp); err != nil {
				var status *HTTPStatusError
				if errors.As(err, &status) && status.Code == 422 {
					// The repository doesn't exist (anymore).
					total.Unavailable = "the repository can't be searched"
					out = append(out, total)
					continue
				}
				return out, err
			}
			total.Value = resp.TotalCount
			total.Tolerance = max(2, int64(float64(resp.TotalCount)*githubReferenceTolerance))
			out = append(out, total)
		}
	}
	return out, nil
}

// githubSearchRetryDelay is how long a search waits after GitHub refused it
// for its rate limit: the search API's limit is per minute.
var githubSearchRetryDelay = time.Minute + 5*time.Second

// githubSearchRetries is how often a refused search is tried again.
const githubSearchRetries = 2

// searchREST makes a search request, and waits and tries again when GitHub
// refuses it for its rate limit, which can happen even when the searches
// are paced, for example when other tools search with the same user.
func (g *GitHubEvents) searchREST(ctx context.Context, u string, out any) error {
	for attempt := 0; ; attempt++ {
		err := g.getREST(ctx, u, out)
		var status *HTTPStatusError
		if attempt == githubSearchRetries || !errors.As(err, &status) || !isRateLimited(status) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(githubSearchRetryDelay):
		}
	}
}

// isRateLimited reports whether GitHub refused a request for a rate limit,
// its primary or a secondary one.
func isRateLimited(err *HTTPStatusError) bool {
	return (err.Code == 403 || err.Code == 429) && strings.Contains(strings.ToLower(err.Message), "rate limit")
}

// ReferenceRequests is how many queries the all-time totals of n
// repositories take.
func (g *GitHubEvents) ReferenceRequests(n int) int {
	return (n + githubReferenceBatch - 1) / githubReferenceBatch
}
