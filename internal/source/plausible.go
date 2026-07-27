package source

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/posit-dev/velocirepo/internal/dateutil"
)

type Plausible struct {
	Client  *http.Client
	APIKey  string
	SiteID  string
	BaseURL string // override for testing
}

func (p *Plausible) Name() string { return "plausible" }

func (p *Plausible) Fetch(ctx context.Context, opts FetchOptions) ([]Record, error) {
	type metricSet struct {
		dimensions []string
		names      []string
	}
	sets := []metricSet{
		{[]string{"time:day"}, []string{"daily_site_pageviews", "daily_site_visitors", "daily_site_visits"}},
		{[]string{"time:day", "event:page"}, []string{"daily_pageviews", "daily_visitors", "daily_visits"}},
	}

	var all []Record
	for _, s := range sets {
		records, err := p.fetchMetrics(ctx, opts, s.dimensions, s.names)
		if err != nil {
			return nil, err
		}
		all = append(all, records...)
	}
	return all, nil
}

func (p *Plausible) fetchMetrics(ctx context.Context, opts FetchOptions, dimensions, metricNames []string) ([]Record, error) {
	const pageSize = 10000

	hasPageDim := len(dimensions) >= 2
	var records []Record
	offset := 0

	for {
		payload := map[string]interface{}{
			"site_id": p.SiteID,
			"metrics": []string{"pageviews", "visitors", "visits"},
			"date_range": []string{
				dateutil.FormatDate(opts.StartDate),
				dateutil.FormatDate(opts.EndDate),
			},
			"dimensions": dimensions,
			"pagination": map[string]int{"limit": pageSize, "offset": offset},
			"include":    map[string]bool{"total_rows": true},
		}

		var result queryResult
		if err := p.query(ctx, payload, &result); err != nil {
			return nil, err
		}

		for _, row := range result.Results {
			if len(row.Dimensions) == 0 || len(row.Metrics) < 3 {
				continue
			}
			date := row.Dimensions[0]
			var extra map[string]string
			if hasPageDim && len(row.Dimensions) >= 2 {
				extra = map[string]string{"page": row.Dimensions[1]}
			}
			for i, name := range metricNames {
				records = append(records, Record{
					Metric:    name,
					ProjectID: opts.ProjectID,
					Target:    p.SiteID,
					Date:      date,
					Value:     row.Metrics[i],
					Extra:     extra,
				})
			}
		}

		offset += pageSize
		if result.Meta.TotalRows == 0 || offset >= result.Meta.TotalRows {
			break
		}
	}

	return records, nil
}

type queryResult struct {
	Results []struct {
		Dimensions []string `json:"dimensions"`
		Metrics    []int64  `json:"metrics"`
	} `json:"results"`
	Meta struct {
		TotalRows int `json:"total_rows"`
	} `json:"meta"`
}

func (p *Plausible) query(ctx context.Context, payload interface{}, result interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	baseURL := "https://plausible.io"
	if p.BaseURL != "" {
		baseURL = p.BaseURL
	}

	return doJSONInto(ctx, p.Client, httpJSONRequest{
		Method: http.MethodPost,
		URL:    baseURL + "/api/v2/query",
		Headers: map[string]string{
			"Authorization": "Bearer " + p.APIKey,
			"Content-Type":  "application/json",
		},
		Body:         bytes.NewReader(body),
		RequestError: "request plausible",
		StatusError:  "plausible returned",
	}, result)
}
