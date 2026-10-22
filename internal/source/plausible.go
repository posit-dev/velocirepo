package source

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/posit-dev/velocirepo/internal/dateutil"
)

type Plausible struct {
	Client  *http.Client
	APIKey  string
	SiteID  string
	BaseURL string // override for testing
	// TimeZone is the site's time zone from the config, if set there. It
	// overrides the Sites API.
	TimeZone string

	zone *time.Location
}

func (p *Plausible) Name() string { return "plausible" }

// latestTimeZone is UTC-12, the latest time zone there is: once a day has
// ended there, it has ended everywhere.
var latestTimeZone = time.FixedZone("UTC-12", -12*3600)

// DayZone returns the time zone the site's days end in. Plausible reports
// days in each site's own time zone, which the Sites API returns. Without
// access to it, and without a configured zone, the site is treated as being
// in UTC-12, so that no partial day is ever stored.
func (p *Plausible) DayZone(ctx context.Context, series string) (*time.Location, error) {
	if p.zone != nil {
		return p.zone, nil
	}
	if p.TimeZone != "" {
		loc, err := time.LoadLocation(p.TimeZone)
		if err != nil {
			return nil, fmt.Errorf("time zone of %s: %w", p.SiteID, err)
		}
		p.zone = loc
		return loc, nil
	}

	var site struct {
		TimeZone string `json:"timezone"`
	}
	err := doJSONInto(ctx, p.Client, httpJSONRequest{
		URL:          p.baseURL() + "/api/v1/sites/" + url.PathEscape(p.SiteID),
		Headers:      map[string]string{"Authorization": "Bearer " + p.APIKey},
		RequestError: "request plausible site",
		StatusError:  "plausible sites API returned",
	}, &site)
	p.zone = latestTimeZone
	if err == nil && site.TimeZone != "" {
		if loc, lerr := time.LoadLocation(site.TimeZone); lerr == nil {
			p.zone = loc
		}
	}
	if p.zone == latestTimeZone {
		slog.Warn("plausible site's time zone unknown; assuming UTC-12, so its days arrive a day later",
			"site", p.SiteID, "hint", "set it under [settings.timezones]")
	}
	return p.zone, nil
}

func (p *Plausible) baseURL() string {
	if p.BaseURL != "" {
		return p.BaseURL
	}
	return "https://plausible.io"
}

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

	// Without an explicit order, pages can be sorted differently, so rows
	// repeat on one page and go missing from another. Sorting by every
	// dimension makes the order total.
	orderBy := make([][]string, len(dimensions))
	for i, d := range dimensions {
		orderBy[i] = []string{d, "asc"}
	}

	for {
		payload := map[string]interface{}{
			"site_id": p.SiteID,
			"metrics": []string{"pageviews", "visitors", "visits"},
			"date_range": []string{
				dateutil.FormatDate(opts.StartDate),
				dateutil.FormatDate(opts.EndDate),
			},
			"dimensions": dimensions,
			"order_by":   orderBy,
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
			// Drop rows whose date can't be parsed or falls outside [StartDate, EndDate].
			t, err := dateutil.ParseDate(date)
			if err != nil || !inDateRange(t, opts.StartDate, opts.EndDate) {
				continue
			}
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

	return doJSONInto(ctx, p.Client, httpJSONRequest{
		Method: http.MethodPost,
		URL:    p.baseURL() + "/api/v2/query",
		Headers: map[string]string{
			"Authorization": "Bearer " + p.APIKey,
			"Content-Type":  "application/json",
		},
		Body:         bytes.NewReader(body),
		RequestError: "request plausible",
		StatusError:  "plausible returned",
	}, result)
}

func init() {
	Register(Meta{
		Name:      "plausible",
		Freshness: map[string]int{"": 2},
		// Plausible keeps the full history.
		Archive:    NotArchived,
		Reconciles: true,
		Target:     "a Plausible site, as its domain",
		Metrics: []MetricInfo{
			{Name: "daily_site_pageviews", Kind: MetricDaily, Description: "pageviews of the whole site that day", Measure: "pageviews", MeasureDescription: "Pageviews of the whole sites."},
			{Name: "daily_site_visitors", Kind: MetricDaily, Description: "unique visitors of the whole site that day", Measure: "visitors", MeasureDescription: "Daily unique visitors of the whole sites, summed: visitor-days, not distinct people."},
			{Name: "daily_site_visits", Kind: MetricDaily, Description: "visits (sessions) of the whole site that day", Measure: "visits", MeasureDescription: "Visits (sessions) of the whole sites."},
			{Name: "daily_pageviews", Kind: MetricDaily, Key: "page", Description: "pageviews of one page that day"},
			{Name: "daily_visitors", Kind: MetricDaily, Key: "page", Description: "unique visitors of one page that day"},
			{Name: "daily_visits", Kind: MetricDaily, Key: "page", Description: "visits (sessions) that included one page that day"},
		},
		Columns: []ColumnInfo{
			{Table: "metrics", Key: "page", Type: "string", Description: "the URL path of a per-page Plausible row"},
		},
		Notes: "Days end in the site's time zone. Per-page pageviews add up to the site's pageviews, but per-page visitors don't add up to the site's visitors: a visitor of two pages counts once for the site. " +
			"Daily unique visitors don't add up over days either.",
	})
}
