package source

import (
	"context"
	"fmt"
	"net/http"
	"time"

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

// PublishedThrough returns the latest day cranlogs has published. CRAN's
// downloads are published two days late, and days without downloads are left
// out, so this is the only way to tell an unpublished day from a quiet one.
// It's the same for every package.
func (c *CRAN) PublishedThrough(ctx context.Context) (time.Time, error) {
	result, err := doJSON[[]struct {
		End string `json:"end"`
	}](ctx, c.Client, httpJSONRequest{
		URL:          fmt.Sprintf("%s/downloads/total/last-day/%s", c.baseURL(), c.Package),
		RequestError: "request cranlogs",
		StatusError:  "cranlogs returned",
	})
	if err != nil {
		return time.Time{}, err
	}
	if len(result) == 0 || result[0].End == "" {
		return time.Time{}, fmt.Errorf("cranlogs returned no last day")
	}
	return dateutil.ParseDate(result[0].End)
}
