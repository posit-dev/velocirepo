package source

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/posit-dev/velocirepo/internal/dateutil"
)

type PyPI struct {
	Client  *http.Client
	Package string
	BaseURL string // override for testing
}

func (p *PyPI) Name() string { return "pypi" }

func (p *PyPI) baseURL() string {
	if p.BaseURL != "" {
		return p.BaseURL
	}
	return "https://pypistats.org"
}

func (p *PyPI) Fetch(ctx context.Context, opts FetchOptions) ([]Record, error) {
	url := fmt.Sprintf("%s/api/packages/%s/overall?mirrors=false", p.baseURL(), p.Package)

	result, err := doJSON[struct {
		Data []struct {
			Category  string `json:"category"`
			Date      string `json:"date"`
			Downloads int64  `json:"downloads"`
		} `json:"data"`
	}](ctx, p.Client, httpJSONRequest{
		URL:          url,
		RequestError: "request pypistats",
		StatusError:  "pypistats returned",
	})
	if err != nil {
		return nil, err
	}

	var records []Record
	for _, entry := range result.Data {
		if entry.Category == "with_mirrors" {
			continue
		}
		entryDate, err := dateutil.ParseDate(entry.Date)
		if err != nil {
			continue
		}
		if !inDateRange(entryDate, opts.StartDate, opts.EndDate) {
			continue
		}
		records = append(records, Record{
			Metric:    "daily_downloads",
			ProjectID: opts.ProjectID,
			Target:    p.Package,
			Date:      entry.Date,
			Value:     entry.Downloads,
		})
	}

	return records, nil
}

func init() {
	Register(Meta{
		Name:      "pypi",
		Freshness: map[string]int{"": 2},
		// pypistats keeps about 180 days.
		Archive: ArchiveFull,
		// pypistats' recent totals of a week and a month don't add up to
		// its daily data, which they're computed over other windows than;
		// its last day is the data itself.
		NoReferenceTotals: "pypistats' recent totals aren't computed over the daily data's windows",
		Target:            "a PyPI package name",
		Metrics: []MetricInfo{
			{Name: "daily_downloads", Kind: MetricDaily, Description: "package downloads that day, without known mirrors (pypistats)", Measure: "pypi_downloads", MeasureDescription: "PyPI downloads without known mirrors. Includes automated traffic such as CI; indicates scale and trend, not users. Don't add to the downloads of another registry."},
		},
		Notes: "PyPI downloads come from pypistats, without known mirrors. They include automated traffic such as CI and caching, so they indicate scale and trend, not user counts. " +
			"pypistats keeps about 180 days, so a series starts at most 180 days before collection began.",
	})
}

// pypiReferencePackage has downloads every day, so the latest day pypistats
// reports for it is the latest day it has published.
const pypiReferencePackage = "pip"

// PublishedThrough returns the latest day pypistats has published. Days
// without downloads are left out, so a quiet package's own latest day says
// nothing about it; the reference package's does.
func (p *PyPI) PublishedThrough(ctx context.Context) (time.Time, error) {
	result, err := doJSON[struct {
		Data []struct {
			Category string `json:"category"`
			Date     string `json:"date"`
		} `json:"data"`
	}](ctx, p.Client, httpJSONRequest{
		URL:          fmt.Sprintf("%s/api/packages/%s/overall?mirrors=false", p.baseURL(), pypiReferencePackage),
		RequestError: "request pypistats",
		StatusError:  "pypistats returned",
	})
	if err != nil {
		return time.Time{}, err
	}
	latest := ""
	for _, entry := range result.Data {
		if entry.Category == "without_mirrors" && entry.Date > latest {
			latest = entry.Date
		}
	}
	if latest == "" {
		return time.Time{}, fmt.Errorf("pypistats returned no days for %s", pypiReferencePackage)
	}
	return dateutil.ParseDate(latest)
}
