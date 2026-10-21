package source

import (
	"context"
	"fmt"
	"net/http"

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
		Archive:           ArchiveFull,
		NoReferenceTotals: "not implemented yet",
	})
}
