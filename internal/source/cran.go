package source

import (
	"context"
	"fmt"
	"net/http"

	"github.com/posit-dev/velocirepo/internal/dateutil"
)

type CRAN struct {
	Client  *http.Client
	Package string
	BaseURL string // override for testing
}

func (c *CRAN) Name() string { return "cran" }

func (c *CRAN) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://cranlogs.r-pkg.org"
}

func (c *CRAN) Fetch(ctx context.Context, opts FetchOptions) ([]Record, error) {
	startStr := dateutil.FormatDate(opts.StartDate)
	endStr := dateutil.FormatDate(opts.EndDate)
	url := fmt.Sprintf("%s/downloads/daily/%s:%s/%s", c.baseURL(), startStr, endStr, c.Package)

	result, err := doJSON[[]struct {
		Downloads []struct {
			Day       string `json:"day"`
			Downloads int64  `json:"downloads"`
		} `json:"downloads"`
	}](ctx, c.Client, httpJSONRequest{
		URL:          url,
		RequestError: "request cranlogs",
		StatusError:  "cranlogs returned",
	})
	if err != nil {
		return nil, err
	}

	if len(result) == 0 {
		return nil, nil
	}

	var records []Record
	for _, entry := range result[0].Downloads {
		entryDate, err := dateutil.ParseDate(entry.Day)
		if err != nil {
			continue
		}
		if !inDateRange(entryDate, opts.StartDate, opts.EndDate) {
			continue
		}
		records = append(records, Record{
			Metric:    "daily_downloads",
			ProjectID: opts.ProjectID,
			Target:    c.Package,
			Date:      entry.Day,
			Value:     entry.Downloads,
		})
	}

	return records, nil
}

func init() {
	Register(Meta{
		Name:      "cran",
		Freshness: map[string]int{"": 3},
		// cranlogs keeps the full history and publishes two days late.
		Archive:           NotArchived,
		NoReferenceTotals: "not implemented yet",
	})
}
