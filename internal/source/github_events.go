package source

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type GitHubEvents struct {
	Client  *http.Client
	Token   string
	Repo    string
	BaseURL string

	contentEntries map[string][]ContentEntry
	records        []Record
}

func (g *GitHubEvents) Name() string { return "github" }

func (g *GitHubEvents) ContentByFilename() map[string][]ContentEntry {
	return g.contentEntries
}

func (g *GitHubEvents) Records() []Record {
	return g.records
}

func (g *GitHubEvents) graphqlURL() string {
	if g.BaseURL != "" {
		return g.BaseURL
	}
	return "https://api.github.com/graphql"
}

func (g *GitHubEvents) FetchEvents(ctx context.Context, opts FetchOptions) ([]Event, error) {
	owner, repo := splitOwnerRepo(g.Repo)
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("invalid github repo: %q", g.Repo)
	}

	g.contentEntries = make(map[string][]ContentEntry)

	var events []Event

	stars, err := g.fetchStargazers(ctx, owner, repo, opts)
	if err != nil {
		return nil, fmt.Errorf("stargazers: %w", err)
	}
	events = append(events, stars...)

	forks, err := g.fetchForks(ctx, owner, repo, opts)
	if err != nil {
		return nil, fmt.Errorf("forks: %w", err)
	}
	events = append(events, forks...)

	issues, issueContent, err := g.fetchIssues(ctx, owner, repo, opts)
	if err != nil {
		return nil, fmt.Errorf("issues: %w", err)
	}
	events = append(events, issues...)
	if len(issueContent) > 0 {
		g.contentEntries["issues.jsonl"] = issueContent
	}

	prs, prContent, err := g.fetchPullRequests(ctx, owner, repo, opts)
	if err != nil {
		return nil, fmt.Errorf("pull requests: %w", err)
	}
	events = append(events, prs...)
	if len(prContent) > 0 {
		g.contentEntries["prs.jsonl"] = prContent
	}

	comments, err := g.fetchComments(ctx, owner, repo, opts)
	if err != nil {
		return nil, fmt.Errorf("comments: %w", err)
	}
	events = append(events, comments...)

	allContent := append(issueContent, prContent...)
	reactionRecords, err := g.fetchReactionCounts(ctx, owner, repo, opts, allContent)
	if err != nil {
		return nil, fmt.Errorf("reaction counts: %w", err)
	}
	g.records = reactionRecords

	repoContent, err := g.fetchRepoInfo(ctx, owner, repo, opts)
	if err != nil {
		return nil, fmt.Errorf("repo info: %w", err)
	}
	if len(repoContent) > 0 {
		g.contentEntries["repos.jsonl"] = repoContent
	}

	return events, nil
}

type githubPageReader func([]byte) ([]Event, pageInfo, bool, error)

func (g *GitHubEvents) fetchPaginatedEvents(ctx context.Context, owner, repo, query string, readPage githubPageReader) ([]Event, error) {
	var events []Event
	var cursor *string

	for {
		vars := map[string]interface{}{"owner": owner, "name": repo, "after": cursor}
		resp, err := g.doGraphQL(ctx, query, vars)
		if err != nil {
			return nil, err
		}

		pageEvents, pi, done, err := readPage(resp)
		if err != nil {
			return nil, err
		}
		events = append(events, pageEvents...)

		if done || !pi.HasNextPage {
			break
		}
		cursor = &pi.EndCursor
	}

	return events, nil
}

func (g *GitHubEvents) fetchStargazers(ctx context.Context, owner, repo string, opts FetchOptions) ([]Event, error) {
	query := `query($owner: String!, $name: String!, $after: String) {
		repository(owner: $owner, name: $name) {
			stargazers(first: 100, after: $after, orderBy: {field: STARRED_AT, direction: DESC}) {
				edges {
					starredAt
					node { login }
				}
				pageInfo { hasNextPage endCursor }
			}
		}
	}`

	return g.fetchPaginatedEvents(ctx, owner, repo, query, func(resp []byte) ([]Event, pageInfo, bool, error) {
		var result struct {
			Data struct {
				Repository struct {
					Stargazers struct {
						Edges []struct {
							StarredAt string `json:"starredAt"`
							Node      struct {
								Login string `json:"login"`
							} `json:"node"`
						} `json:"edges"`
						PageInfo pageInfo `json:"pageInfo"`
					} `json:"stargazers"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp, &result); err != nil {
			return nil, pageInfo{}, false, fmt.Errorf("unmarshal stargazers: %w", err)
		}

		var events []Event
		done := false
		for _, edge := range result.Data.Repository.Stargazers.Edges {
			include, stop := includeGitHubEventTime(edge.StarredAt, opts)
			if stop {
				done = true
				break
			}
			if !include {
				continue
			}
			events = append(events, githubEvent(opts, g.Repo, "star", edge.StarredAt, nil, edge.Node.Login, nil))
		}

		return events, result.Data.Repository.Stargazers.PageInfo, done, nil
	})
}

func (g *GitHubEvents) fetchForks(ctx context.Context, owner, repo string, opts FetchOptions) ([]Event, error) {
	query := `query($owner: String!, $name: String!, $after: String) {
		repository(owner: $owner, name: $name) {
			forks(first: 100, after: $after, orderBy: {field: CREATED_AT, direction: DESC}) {
				nodes {
					createdAt
					owner { login }
				}
				pageInfo { hasNextPage endCursor }
			}
		}
	}`

	return g.fetchPaginatedEvents(ctx, owner, repo, query, func(resp []byte) ([]Event, pageInfo, bool, error) {
		var result struct {
			Data struct {
				Repository struct {
					Forks struct {
						Nodes []struct {
							CreatedAt string `json:"createdAt"`
							Owner     struct {
								Login string `json:"login"`
							} `json:"owner"`
						} `json:"nodes"`
						PageInfo pageInfo `json:"pageInfo"`
					} `json:"forks"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp, &result); err != nil {
			return nil, pageInfo{}, false, fmt.Errorf("unmarshal forks: %w", err)
		}

		var events []Event
		done := false
		for _, node := range result.Data.Repository.Forks.Nodes {
			include, stop := includeGitHubEventTime(node.CreatedAt, opts)
			if stop {
				done = true
				break
			}
			if !include {
				continue
			}
			events = append(events, githubEvent(opts, g.Repo, "fork", node.CreatedAt, nil, node.Owner.Login, nil))
		}

		return events, result.Data.Repository.Forks.PageInfo, done, nil
	})
}

func (g *GitHubEvents) fetchIssues(ctx context.Context, owner, repo string, opts FetchOptions) ([]Event, []ContentEntry, error) {
	query := `query($owner: String!, $name: String!, $after: String) {
		repository(owner: $owner, name: $name) {
			issues(first: 100, after: $after, orderBy: {field: CREATED_AT, direction: DESC}) {
				nodes {
					number
					title
					body
					state
					createdAt
					closedAt
					url
					author { login }
					labels(first: 10) { nodes { name } }
				}
				pageInfo { hasNextPage endCursor }
			}
		}
	}`

	var content []ContentEntry
	events, err := g.fetchPaginatedEvents(ctx, owner, repo, query, func(resp []byte) ([]Event, pageInfo, bool, error) {
		var result struct {
			Data struct {
				Repository struct {
					Issues struct {
						Nodes []struct {
							Number    int     `json:"number"`
							Title     string  `json:"title"`
							Body      string  `json:"body"`
							State     string  `json:"state"`
							CreatedAt string  `json:"createdAt"`
							ClosedAt  *string `json:"closedAt"`
							URL       string  `json:"url"`
							Author    *struct {
								Login string `json:"login"`
							} `json:"author"`
							Labels struct {
								Nodes []struct {
									Name string `json:"name"`
								} `json:"nodes"`
							} `json:"labels"`
						} `json:"nodes"`
						PageInfo pageInfo `json:"pageInfo"`
					} `json:"issues"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp, &result); err != nil {
			return nil, pageInfo{}, false, fmt.Errorf("unmarshal issues: %w", err)
		}

		var events []Event
		done := false
		for _, node := range result.Data.Repository.Issues.Nodes {
			include, stop := includeGitHubEventTime(node.CreatedAt, opts)
			if stop {
				done = true
				break
			}

			var login string
			if node.Author != nil && node.Author.Login != "" {
				login = node.Author.Login
			}
			ref := node.Number

			entry := ContentEntry{
				Source:      "github",
				ProjectID:   opts.ProjectID,
				Target:      g.Repo,
				ID:          fmt.Sprintf("issue/%d", node.Number),
				Ref:         &ref,
				Title:       node.Title,
				Description: node.Body,
				PublishedAt: node.CreatedAt,
				URL:         node.URL,
				Type:        "issue",
			}
			if login != "" {
				entry.Extra = map[string]any{"author": login}
			}
			if node.State != "" {
				if entry.Extra == nil {
					entry.Extra = map[string]any{}
				}
				entry.Extra["state"] = node.State
			}
			var labels []string
			for _, l := range node.Labels.Nodes {
				labels = append(labels, l.Name)
			}
			if len(labels) > 0 {
				entry.Tags = labels
			}
			if node.ClosedAt != nil {
				entry.UpdatedAt = *node.ClosedAt
			}
			content = append(content, entry)

			if !include {
				continue
			}
			events = append(events, githubEvent(opts, g.Repo, "issue_open", node.CreatedAt, &ref, login, nil))

			if node.ClosedAt != nil {
				events = appendOptionalGitHubEvent(events, opts, g.Repo, "issue_close", *node.ClosedAt, &ref, login)
			}
		}

		return events, result.Data.Repository.Issues.PageInfo, done, nil
	})

	return events, content, err
}

func (g *GitHubEvents) fetchPullRequests(ctx context.Context, owner, repo string, opts FetchOptions) ([]Event, []ContentEntry, error) {
	query := `query($owner: String!, $name: String!, $after: String) {
		repository(owner: $owner, name: $name) {
			pullRequests(first: 100, after: $after, orderBy: {field: CREATED_AT, direction: DESC}) {
				nodes {
					number
					title
					body
					state
					createdAt
					closedAt
					mergedAt
					url
					author { login }
					labels(first: 10) { nodes { name } }
				}
				pageInfo { hasNextPage endCursor }
			}
		}
	}`

	var content []ContentEntry
	events, err := g.fetchPaginatedEvents(ctx, owner, repo, query, func(resp []byte) ([]Event, pageInfo, bool, error) {
		var result struct {
			Data struct {
				Repository struct {
					PullRequests struct {
						Nodes []struct {
							Number    int     `json:"number"`
							Title     string  `json:"title"`
							Body      string  `json:"body"`
							State     string  `json:"state"`
							CreatedAt string  `json:"createdAt"`
							ClosedAt  *string `json:"closedAt"`
							MergedAt  *string `json:"mergedAt"`
							URL       string  `json:"url"`
							Author    *struct {
								Login string `json:"login"`
							} `json:"author"`
							Labels struct {
								Nodes []struct {
									Name string `json:"name"`
								} `json:"nodes"`
							} `json:"labels"`
						} `json:"nodes"`
						PageInfo pageInfo `json:"pageInfo"`
					} `json:"pullRequests"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp, &result); err != nil {
			return nil, pageInfo{}, false, fmt.Errorf("unmarshal pull requests: %w", err)
		}

		var events []Event
		done := false
		for _, node := range result.Data.Repository.PullRequests.Nodes {
			include, stop := includeGitHubEventTime(node.CreatedAt, opts)
			if stop {
				done = true
				break
			}

			var login string
			if node.Author != nil && node.Author.Login != "" {
				login = node.Author.Login
			}
			ref := node.Number

			entry := ContentEntry{
				Source:      "github",
				ProjectID:   opts.ProjectID,
				Target:      g.Repo,
				ID:          fmt.Sprintf("pr/%d", node.Number),
				Ref:         &ref,
				Title:       node.Title,
				Description: node.Body,
				PublishedAt: node.CreatedAt,
				URL:         node.URL,
				Type:        "pr",
			}
			extra := map[string]any{}
			if login != "" {
				extra["author"] = login
			}
			if node.State != "" {
				extra["state"] = node.State
			}
			if node.MergedAt != nil {
				extra["merged_at"] = *node.MergedAt
			}
			if len(extra) > 0 {
				entry.Extra = extra
			}
			var labels []string
			for _, l := range node.Labels.Nodes {
				labels = append(labels, l.Name)
			}
			if len(labels) > 0 {
				entry.Tags = labels
			}
			if node.MergedAt != nil {
				entry.UpdatedAt = *node.MergedAt
			} else if node.ClosedAt != nil {
				entry.UpdatedAt = *node.ClosedAt
			}
			content = append(content, entry)

			if !include {
				continue
			}
			events = append(events, githubEvent(opts, g.Repo, "pr_open", node.CreatedAt, &ref, login, nil))

			if node.MergedAt != nil {
				events = appendOptionalGitHubEvent(events, opts, g.Repo, "pr_merge", *node.MergedAt, &ref, login)
			}
		}

		return events, result.Data.Repository.PullRequests.PageInfo, done, nil
	})

	return events, content, err
}

func (g *GitHubEvents) fetchComments(ctx context.Context, owner, repo string, opts FetchOptions) ([]Event, error) {
	var events []Event
	page := 1

	for {
		url := fmt.Sprintf("%s/repos/%s/%s/issues/comments?since=%s&sort=created&direction=desc&per_page=100&page=%d",
			g.restBaseURL(), owner, repo, opts.StartDate.Format("2006-01-02T15:04:05Z"), page)

		var comments []struct {
			CreatedAt string `json:"created_at"`
			IssueURL  string `json:"issue_url"`
			User      *struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if err := g.getREST(ctx, url, &comments); err != nil {
			return nil, err
		}

		if len(comments) == 0 {
			break
		}

		for _, c := range comments {
			t, err := time.Parse(time.RFC3339, c.CreatedAt)
			if err != nil {
				continue
			}
			if t.Before(opts.StartDate) {
				continue
			}
			if !inDateRange(t, opts.StartDate, opts.EndDate) {
				continue
			}

			ref := extractIssueNumber(c.IssueURL)
			var user string
			if c.User != nil {
				user = c.User.Login
			}
			events = append(events, githubEvent(opts, g.Repo, "comment", c.CreatedAt, ref, user, nil))
		}

		if len(comments) < 100 {
			break
		}
		page++
	}

	return events, nil
}

const reactionCountBatchSize = 10

func (g *GitHubEvents) fetchReactionCounts(ctx context.Context, owner, repo string, opts FetchOptions, content []ContentEntry) ([]Record, error) {
	knownRefs := g.readKnownReactionRefs(opts)

	var numbers []int
	for _, entry := range content {
		if entry.Ref == nil {
			continue
		}
		ref := *entry.Ref
		state, _ := entry.Extra["state"].(string)
		isOpen := state == "OPEN"
		if isOpen || !knownRefs[ref] {
			numbers = append(numbers, ref)
		}
	}

	if len(numbers) == 0 {
		return nil, nil
	}

	date := opts.EndDate.Format("2006-01-02")
	var records []Record

	for i := 0; i < len(numbers); i += reactionCountBatchSize {
		end := i + reactionCountBatchSize
		if end > len(numbers) {
			end = len(numbers)
		}
		batch := numbers[i:end]

		counts, err := g.fetchReactionCountBatch(ctx, owner, repo, batch)
		if err != nil {
			return nil, err
		}

		for _, rc := range counts {
			records = append(records, Record{
				Metric:    "total_reactions",
				ProjectID: opts.ProjectID,
				Target:    g.Repo,
				Date:      date,
				Value:     rc.count,
				Extra:     map[string]string{"ref": strconv.Itoa(rc.number)},
			})
		}
	}

	return records, nil
}

type reactionCount struct {
	number int
	count  int64
}

func (g *GitHubEvents) fetchReactionCountBatch(ctx context.Context, owner, repo string, numbers []int) ([]reactionCount, error) {
	query := buildReactionCountQuery(numbers)
	vars := map[string]interface{}{"owner": owner, "name": repo}
	resp, err := g.doGraphQL(ctx, query, vars)
	if err != nil {
		return nil, fmt.Errorf("reaction counts batch: %w", err)
	}

	var raw struct {
		Data struct {
			Repository map[string]json.RawMessage `json:"repository"`
		} `json:"data"`
	}

	// Unmarshal with repository as raw JSON to handle dynamic aliases
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(resp, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal reaction count response: %w", err)
	}
	dataRaw, ok := envelope["data"]
	if !ok {
		return nil, fmt.Errorf("no data in reaction count response")
	}
	var dataObj map[string]json.RawMessage
	if err := json.Unmarshal(dataRaw, &dataObj); err != nil {
		return nil, fmt.Errorf("unmarshal data: %w", err)
	}
	repoRaw, ok := dataObj["repository"]
	if !ok {
		return nil, fmt.Errorf("no repository in reaction count response")
	}
	var repoObj map[string]json.RawMessage
	if err := json.Unmarshal(repoRaw, &repoObj); err != nil {
		return nil, fmt.Errorf("unmarshal repository: %w", err)
	}
	_ = raw

	var results []reactionCount
	var overflow []int

	for idx, number := range numbers {
		alias := fmt.Sprintf("r%d", idx)
		itemRaw, ok := repoObj[alias]
		if !ok || string(itemRaw) == "null" {
			results = append(results, reactionCount{number: number, count: 0})
			continue
		}

		var item struct {
			Reactions struct {
				TotalCount int64 `json:"totalCount"`
			} `json:"reactions"`
			Comments struct {
				Nodes []struct {
					Reactions struct {
						TotalCount int64 `json:"totalCount"`
					} `json:"reactions"`
				} `json:"nodes"`
				PageInfo pageInfo `json:"pageInfo"`
			} `json:"comments"`
		}
		if err := json.Unmarshal(itemRaw, &item); err != nil {
			return nil, fmt.Errorf("unmarshal item r%d (#%d): %w", idx, number, err)
		}

		total := item.Reactions.TotalCount
		for _, c := range item.Comments.Nodes {
			total += c.Reactions.TotalCount
		}

		if item.Comments.PageInfo.HasNextPage {
			overflow = append(overflow, number)
		}

		results = append(results, reactionCount{number: number, count: total})
	}

	// Handle overflow: items with >100 comments need pagination
	if len(overflow) > 0 {
		overflowCounts, err := g.fetchOverflowCommentReactions(ctx, owner, repo, overflow)
		if err != nil {
			return nil, err
		}
		for i, rc := range results {
			if extra, ok := overflowCounts[rc.number]; ok {
				results[i].count += extra
			}
		}
	}

	return results, nil
}

func buildReactionCountQuery(numbers []int) string {
	var b strings.Builder
	b.WriteString("query($owner: String!, $name: String!) {\n  repository(owner: $owner, name: $name) {\n")
	for i, n := range numbers {
		fmt.Fprintf(&b, "    r%d: issueOrPullRequest(number: %d) {\n", i, n)
		b.WriteString("      ... on Issue {\n")
		b.WriteString("        reactions { totalCount }\n")
		b.WriteString("        comments(first: 100) {\n")
		b.WriteString("          nodes { reactions { totalCount } }\n")
		b.WriteString("          pageInfo { hasNextPage endCursor }\n")
		b.WriteString("        }\n")
		b.WriteString("      }\n")
		b.WriteString("      ... on PullRequest {\n")
		b.WriteString("        reactions { totalCount }\n")
		b.WriteString("        comments(first: 100) {\n")
		b.WriteString("          nodes { reactions { totalCount } }\n")
		b.WriteString("          pageInfo { hasNextPage endCursor }\n")
		b.WriteString("        }\n")
		b.WriteString("      }\n")
		b.WriteString("    }\n")
	}
	b.WriteString("  }\n}")
	return b.String()
}

func (g *GitHubEvents) fetchOverflowCommentReactions(ctx context.Context, owner, repo string, numbers []int) (map[int]int64, error) {
	result := make(map[int]int64)

	query := `query($owner: String!, $name: String!, $number: Int!, $after: String) {
		repository(owner: $owner, name: $name) {
			issueOrPullRequest(number: $number) {
				... on Issue {
					comments(first: 100, after: $after) {
						nodes { reactions { totalCount } }
						pageInfo { hasNextPage endCursor }
					}
				}
				... on PullRequest {
					comments(first: 100, after: $after) {
						nodes { reactions { totalCount } }
						pageInfo { hasNextPage endCursor }
					}
				}
			}
		}
	}`

	for _, number := range numbers {
		// We already counted the first 100 comments in the batch query,
		// so start from the second page.
		var extra int64
		var cursor *string
		firstPage := true

		for {
			if firstPage {
				// Skip the first page — already counted in the batch
				// We need to get the cursor for page 2 by re-fetching page 1
				vars := map[string]interface{}{"owner": owner, "name": repo, "number": number, "after": nil}
				resp, err := g.doGraphQL(ctx, query, vars)
				if err != nil {
					return nil, fmt.Errorf("overflow reactions for #%d: %w", number, err)
				}
				pi, err := parseCommentPageInfo(resp)
				if err != nil {
					return nil, err
				}
				if !pi.HasNextPage {
					break
				}
				cursor = &pi.EndCursor
				firstPage = false
				continue
			}

			vars := map[string]interface{}{"owner": owner, "name": repo, "number": number, "after": cursor}
			resp, err := g.doGraphQL(ctx, query, vars)
			if err != nil {
				return nil, fmt.Errorf("overflow reactions for #%d: %w", number, err)
			}

			count, pi, err := parseCommentReactionCounts(resp)
			if err != nil {
				return nil, err
			}
			extra += count

			if !pi.HasNextPage {
				break
			}
			cursor = &pi.EndCursor
		}

		result[number] = extra
	}

	return result, nil
}

func parseCommentPageInfo(resp []byte) (pageInfo, error) {
	var result struct {
		Data struct {
			Repository struct {
				IssueOrPullRequest struct {
					Comments struct {
						PageInfo pageInfo `json:"pageInfo"`
					} `json:"comments"`
				} `json:"issueOrPullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return pageInfo{}, err
	}
	return result.Data.Repository.IssueOrPullRequest.Comments.PageInfo, nil
}

func parseCommentReactionCounts(resp []byte) (int64, pageInfo, error) {
	var result struct {
		Data struct {
			Repository struct {
				IssueOrPullRequest struct {
					Comments struct {
						Nodes []struct {
							Reactions struct {
								TotalCount int64 `json:"totalCount"`
							} `json:"reactions"`
						} `json:"nodes"`
						PageInfo pageInfo `json:"pageInfo"`
					} `json:"comments"`
				} `json:"issueOrPullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return 0, pageInfo{}, err
	}
	var total int64
	for _, n := range result.Data.Repository.IssueOrPullRequest.Comments.Nodes {
		total += n.Reactions.TotalCount
	}
	return total, result.Data.Repository.IssueOrPullRequest.Comments.PageInfo, nil
}

func (g *GitHubEvents) readKnownReactionRefs(opts FetchOptions) map[int]bool {
	known := make(map[int]bool)
	if opts.DataDir == "" {
		return known
	}

	dir := filepath.Join(opts.DataDir, "metrics", "github", opts.ProjectID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return known
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			var rec struct {
				Metric string            `json:"metric"`
				Extra  map[string]string `json:"extra"`
			}
			if json.Unmarshal(scanner.Bytes(), &rec) == nil && rec.Metric == "total_reactions" {
				if ref, err := strconv.Atoi(rec.Extra["ref"]); err == nil {
					known[ref] = true
				}
			}
		}
		f.Close()
	}

	return known
}

func (g *GitHubEvents) restBaseURL() string {
	if g.BaseURL != "" {
		return g.BaseURL
	}
	return "https://api.github.com"
}

func (g *GitHubEvents) getREST(ctx context.Context, url string, out any) error {
	headers := map[string]string{"Accept": "application/vnd.github+json"}
	if g.Token != "" {
		headers["Authorization"] = "Bearer " + g.Token
	}
	return doJSONInto(ctx, g.Client, httpJSONRequest{
		URL:          url,
		Headers:      headers,
		RequestError: "github REST request",
		StatusError:  "github REST API returned",
	}, out)
}

func extractIssueNumber(issueURL string) *int {
	for i := len(issueURL) - 1; i >= 0; i-- {
		if issueURL[i] == '/' {
			numStr := issueURL[i+1:]
			n := 0
			for _, c := range numStr {
				if c < '0' || c > '9' {
					return nil
				}
				n = n*10 + int(c-'0')
			}
			if n > 0 {
				return &n
			}
			return nil
		}
	}
	return nil
}

func (g *GitHubEvents) fetchRepoInfo(ctx context.Context, owner, repo string, opts FetchOptions) ([]ContentEntry, error) {
	query := `query($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) {
			name
			description
			url
			homepageUrl
			createdAt
			pushedAt
			primaryLanguage { name }
			licenseInfo { spdxId }
			repositoryTopics(first: 20) { nodes { topic { name } } }
			defaultBranchRef { name }
			isArchived
		}
	}`

	vars := map[string]interface{}{"owner": owner, "name": repo}
	resp, err := g.doGraphQL(ctx, query, vars)
	if err != nil {
		return nil, err
	}

	var result struct {
		Data struct {
			Repository struct {
				Name            string  `json:"name"`
				Description     string  `json:"description"`
				URL             string  `json:"url"`
				HomepageURL     *string `json:"homepageUrl"`
				CreatedAt       string  `json:"createdAt"`
				PushedAt        string  `json:"pushedAt"`
				PrimaryLanguage *struct {
					Name string `json:"name"`
				} `json:"primaryLanguage"`
				LicenseInfo *struct {
					SpdxID string `json:"spdxId"`
				} `json:"licenseInfo"`
				RepositoryTopics struct {
					Nodes []struct {
						Topic struct {
							Name string `json:"name"`
						} `json:"topic"`
					} `json:"nodes"`
				} `json:"repositoryTopics"`
				DefaultBranchRef *struct {
					Name string `json:"name"`
				} `json:"defaultBranchRef"`
				IsArchived bool `json:"isArchived"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("unmarshal repo info: %w", err)
	}

	r := result.Data.Repository
	entry := ContentEntry{
		Source:      "github",
		ProjectID:   opts.ProjectID,
		Target:      g.Repo,
		ID:          g.Repo,
		Title:       r.Name,
		Description: r.Description,
		PublishedAt: r.CreatedAt,
		UpdatedAt:   r.PushedAt,
		URL:         r.URL,
		Type:        "repo",
	}

	var topics []string
	for _, t := range r.RepositoryTopics.Nodes {
		topics = append(topics, t.Topic.Name)
	}
	if len(topics) > 0 {
		entry.Tags = topics
	}

	extra := map[string]any{}
	if r.PrimaryLanguage != nil {
		extra["language"] = r.PrimaryLanguage.Name
	}
	if r.LicenseInfo != nil && r.LicenseInfo.SpdxID != "" {
		extra["license"] = r.LicenseInfo.SpdxID
	}
	if r.HomepageURL != nil && *r.HomepageURL != "" {
		extra["homepage"] = *r.HomepageURL
	}
	if r.DefaultBranchRef != nil {
		extra["default_branch"] = r.DefaultBranchRef.Name
	}
	if r.IsArchived {
		extra["archived"] = true
	}
	if len(extra) > 0 {
		entry.Extra = extra
	}

	return []ContentEntry{entry}, nil
}


func includeGitHubEventTime(timestamp string, opts FetchOptions) (include bool, stop bool) {
	t, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return false, false
	}
	if t.Before(opts.StartDate) {
		return false, true
	}
	return inDateRange(t, opts.StartDate, opts.EndDate), false
}

func appendOptionalGitHubEvent(events []Event, opts FetchOptions, repo, eventType, timestamp string, ref *int, user string) []Event {
	t, err := time.Parse(time.RFC3339, timestamp)
	if err != nil || !inDateRange(t, opts.StartDate, opts.EndDate) {
		return events
	}
	return append(events, githubEvent(opts, repo, eventType, timestamp, ref, user, nil))
}

func githubEvent(opts FetchOptions, repo, eventType, timestamp string, ref *int, user string, extra map[string]string) Event {
	return Event{
		Type:      eventType,
		ProjectID: opts.ProjectID,
		Target:    repo,
		Datetime:  timestamp,
		Ref:       ref,
		User:      user,
		Extra:     extra,
	}
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type graphqlRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables"`
}

func (g *GitHubEvents) doGraphQL(ctx context.Context, query string, vars map[string]interface{}) ([]byte, error) {
	body, err := json.Marshal(graphqlRequest{Query: query, Variables: vars})
	if err != nil {
		return nil, err
	}

	headers := map[string]string{"Content-Type": "application/json"}
	if g.Token != "" {
		headers["Authorization"] = "Bearer " + g.Token
	}
	data, err := doRequest(ctx, g.Client, httpJSONRequest{
		Method:       http.MethodPost,
		URL:          g.graphqlURL(),
		Headers:      headers,
		Body:         bytes.NewReader(body),
		RequestError: "graphql request",
		StatusError:  "graphql returned",
	})
	if err != nil {
		return nil, err
	}

	var errResp struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(data, &errResp) == nil && len(errResp.Errors) > 0 {
		return nil, fmt.Errorf("graphql error: %s", errResp.Errors[0].Message)
	}

	return data, nil
}
