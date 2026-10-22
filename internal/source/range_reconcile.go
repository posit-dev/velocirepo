package source

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// rangeTolerance is how far stored totals over a complete month may differ
// from the API's own, relative to them, and at least rangeSlack.
const (
	rangeTolerance = 0.01
	rangeSlack     = 2
)

func rangeTotal(target, metric, label string, value int64, from, through time.Time) ReferenceTotal {
	return ReferenceTotal{Target: target, Kind: "records", Metric: metric, Label: label, Value: value,
		Tolerance: max(rangeSlack, int64(float64(value)*rangeTolerance)), From: from, Through: through}
}

var (
	_ MonthlyReconciler = (*CRAN)(nil)
	_ MonthlyReconciler = (*Plausible)(nil)
)

// ReferenceTotals: cranlogs has no all-time totals worth comparing; it's
// reconciled per month.
func (c *CRAN) ReferenceTotals(context.Context, []string) ([]ReferenceTotal, error) { return nil, nil }

func (c *CRAN) MonthlyRequests() int { return 1 }

func (c *CRAN) MonthlyQuota() string { return "" }

// MonthlyReferenceTotals returns cranlogs' download totals of each package
// over a range, which match the sum of its daily counts.
func (c *CRAN) MonthlyReferenceTotals(ctx context.Context, targets []string, from, through time.Time, wait func(context.Context) error) ([]ReferenceTotal, error) {
	var out []ReferenceTotal
	for _, pkg := range targets {
		if wait != nil {
			if err := wait(ctx); err != nil {
				return out, err
			}
		}
		u := fmt.Sprintf("%s/downloads/total/%s:%s/%s", c.baseURL(), from.Format("2006-01-02"), through.Format("2006-01-02"), url.PathEscape(pkg))
		result, err := doJSON[[]struct {
			Downloads int64  `json:"downloads"`
			Package   string `json:"package"`
		}](ctx, c.Client, httpJSONRequest{URL: u, RequestError: "request cranlogs", StatusError: "cranlogs returned"})
		if err != nil {
			return out, err
		}
		if len(result) == 0 {
			out = append(out, ReferenceTotal{Target: pkg, Kind: "records", Metric: "daily_downloads", Unavailable: "cranlogs has no total for the package"})
			continue
		}
		out = append(out, rangeTotal(pkg, "daily_downloads", "downloads", result[0].Downloads, from, through))
	}
	return out, nil
}

// ReferenceTotals: Plausible is reconciled per month.
func (p *Plausible) ReferenceTotals(context.Context, []string) ([]ReferenceTotal, error) {
	return nil, nil
}

func (p *Plausible) MonthlyRequests() int { return 1 }

func (p *Plausible) MonthlyQuota() string { return "plausible.io" }

// MonthlyReferenceTotals returns each site's pageviews and visits over a
// range from an aggregate query without the day dimension, in the site's
// time zone like the stored days. Visitors aren't compared: unique visitors
// don't add up over days.
func (p *Plausible) MonthlyReferenceTotals(ctx context.Context, targets []string, from, through time.Time, wait func(context.Context) error) ([]ReferenceTotal, error) {
	var out []ReferenceTotal
	for _, site := range targets {
		if wait != nil {
			if err := wait(ctx); err != nil {
				return out, err
			}
		}
		payload := map[string]any{
			"site_id":    site,
			"metrics":    []string{"pageviews", "visits"},
			"date_range": []string{from.Format("2006-01-02"), through.Format("2006-01-02")},
		}
		var result struct {
			Results []struct {
				Metrics []int64 `json:"metrics"`
			} `json:"results"`
		}
		if err := p.query(ctx, payload, &result); err != nil {
			if strings.Contains(err.Error(), "404") {
				out = append(out, ReferenceTotal{Target: site, Kind: "records", Metric: "daily_site_pageviews", Unavailable: "the site isn't accessible"})
				continue
			}
			return out, err
		}
		if len(result.Results) == 0 || len(result.Results[0].Metrics) != 2 {
			out = append(out, ReferenceTotal{Target: site, Kind: "records", Metric: "daily_site_pageviews", Unavailable: "Plausible returned no aggregate"})
			continue
		}
		m := result.Results[0].Metrics
		out = append(out,
			rangeTotal(site, "daily_site_pageviews", "pageviews", m[0], from, through),
			rangeTotal(site, "daily_site_visits", "visits", m[1], from, through))
	}
	return out, nil
}
