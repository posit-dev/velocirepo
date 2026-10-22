package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/posit-dev/velocirepo/internal/dateutil"
)

type GitHubTraffic struct {
	Client  *http.Client
	Token   string
	Repo    string
	BaseURL string
}

func (g *GitHubTraffic) Name() string { return "github-traffic" }

func (g *GitHubTraffic) baseURL() string {
	if g.BaseURL != "" {
		return g.BaseURL
	}
	return "https://api.github.com"
}

func (g *GitHubTraffic) Fetch(ctx context.Context, opts FetchOptions) ([]Record, error) {
	owner, repo := splitOwnerRepo(g.Repo)
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("invalid github repo: %q", g.Repo)
	}

	type endpoint struct {
		path        string
		countMetric string
		uniqMetric  string
	}
	endpoints := []endpoint{
		{"traffic/views?per=day", "daily_views", "daily_unique_views"},
		{"traffic/clones?per=day", "daily_clones", "daily_unique_clones"},
	}

	var all []Record
	for _, ep := range endpoints {
		url := fmt.Sprintf("%s/repos/%s/%s/%s", g.baseURL(), owner, repo, ep.path)

		var result trafficResponse
		if err := g.get(ctx, url, &result); err != nil {
			return nil, fmt.Errorf("%s: %w", ep.countMetric, err)
		}

		for _, item := range result.Items {
			t, err := time.Parse(time.RFC3339, item.Timestamp)
			if err != nil {
				continue
			}
			if !inDateRange(t, opts.StartDate, opts.EndDate) {
				continue
			}
			date := dateutil.FormatDate(t)
			all = append(all, Record{
				Metric:    ep.countMetric,
				ProjectID: opts.ProjectID,
				Target:    g.Repo,
				Date:      date,
				Value:     item.Count,
			})
			all = append(all, Record{
				Metric:    ep.uniqMetric,
				ProjectID: opts.ProjectID,
				Target:    g.Repo,
				Date:      date,
				Value:     item.Uniques,
			})
		}
	}

	return all, nil
}

type trafficResponse struct {
	Count   int64         `json:"count"`
	Uniques int64         `json:"uniques"`
	Items   []trafficItem `json:"views,omitempty"`
}

type trafficItem struct {
	Timestamp string `json:"timestamp"`
	Count     int64  `json:"count"`
	Uniques   int64  `json:"uniques"`
}

func (r *trafficResponse) UnmarshalJSON(data []byte) error {
	type raw struct {
		Count   int64         `json:"count"`
		Uniques int64         `json:"uniques"`
		Views   []trafficItem `json:"views"`
		Clones  []trafficItem `json:"clones"`
	}
	var v raw
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	r.Count = v.Count
	r.Uniques = v.Uniques
	if v.Views != nil {
		r.Items = v.Views
	} else {
		r.Items = v.Clones
	}
	return nil
}

func (g *GitHubTraffic) get(ctx context.Context, url string, result interface{}) error {
	headers := map[string]string{"Accept": "application/vnd.github+json"}
	if g.Token != "" {
		headers["Authorization"] = "Bearer " + g.Token
	}
	return doJSONInto(ctx, g.Client, httpJSONRequest{
		URL:          url,
		Headers:      headers,
		RequestError: "request " + url,
		StatusError:  "github traffic API returned",
	}, result)
}

func init() {
	Register(Meta{
		Name:      "github-traffic",
		Freshness: map[string]int{"": 1},
		// GitHub only keeps 14 days of traffic.
		Archive:           ArchiveFull,
		NoReferenceTotals: "GitHub reports no traffic totals beyond the daily counts",
		Target:            "a GitHub repository, as owner/repo; needs a token with push access",
		Metrics: []MetricInfo{
			{Name: "daily_views", Kind: MetricDaily, Description: "views of the repository's pages that day", Measure: "repo_views", MeasureDescription: "Views of the repositories' GitHub pages."},
			{Name: "daily_unique_views", Kind: MetricDaily, Description: "unique visitors of the repository's pages that day"},
			{Name: "daily_clones", Kind: MetricDaily, Description: "clones of the repository that day", Measure: "repo_clones", MeasureDescription: "Clones of the repositories."},
			{Name: "daily_unique_clones", Kind: MetricDaily, Description: "unique cloners of the repository that day"},
		},
		Notes: "GitHub keeps 14 days of traffic, so the series starts at most 14 days before collection began. " +
			"Unique visitors and cloners don't add up over days.",
	})
}
