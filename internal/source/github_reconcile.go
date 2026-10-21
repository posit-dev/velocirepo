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
// all-time totals, relative to them, and at least githubReferenceSlack: items
// opened since the last day fetched, deleted forks, and stars taken back.
const (
	githubReferenceTolerance = 0.01
	githubReferenceSlack     = 5
)

var _ Reconciler = (*GitHubEvents)(nil)

type githubRepoTotals struct {
	Issues         struct{ TotalCount int64 } `json:"issues"`
	PullRequests   struct{ TotalCount int64 } `json:"pullRequests"`
	Merged         struct{ TotalCount int64 } `json:"merged"`
	ForkCount      int64                      `json:"forkCount"`
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
		fmt.Fprintf(&q, ` r%d: repository(owner: %q, name: %q) { issues { totalCount } pullRequests { totalCount } merged: pullRequests(states: MERGED) { totalCount } forkCount stargazerCount }`, i, owner, name)
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
		}{
			{"events", "issue_open", "issues", t.Issues.TotalCount},
			{"events", "pr_open", "pull requests", t.PullRequests.TotalCount},
			{"events", "pr_merge", "merged pull requests", t.Merged.TotalCount},
			{"events", "fork", "forks", t.ForkCount},
			{"records", "daily_stars", "stars", t.StargazerCount},
		} {
			tol := max(int64(githubReferenceSlack), int64(float64(r.value)*githubReferenceTolerance))
			out = append(out, ReferenceTotal{Target: repo, Kind: r.kind, Metric: r.metric, Label: r.label, Value: r.value, Tolerance: tol})
		}
	}
	return out, nil
}
