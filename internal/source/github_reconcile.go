package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
