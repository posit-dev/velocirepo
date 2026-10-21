package source

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/posit-dev/velocirepo/internal/dateutil"
)

type OpenVSX struct {
	Client      *http.Client
	ExtensionID string
	BaseURL     string // override for testing
}

func (o *OpenVSX) Name() string { return "openvsx" }

func (o *OpenVSX) Fetch(ctx context.Context, opts FetchOptions) ([]Record, error) {
	parts := strings.SplitN(o.ExtensionID, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid openvsx extension id: %q (expected namespace/extension)", o.ExtensionID)
	}

	baseURL := "https://open-vsx.org"
	if o.BaseURL != "" {
		baseURL = o.BaseURL
	}
	url := fmt.Sprintf("%s/api/%s/%s", baseURL, parts[0], parts[1])

	result, err := doJSON[struct {
		DownloadCount int64    `json:"downloadCount"`
		AverageRating *float64 `json:"averageRating"`
		ReviewCount   int64    `json:"reviewCount"`
	}](ctx, o.Client, httpJSONRequest{
		URL:          url,
		RequestError: "request openvsx",
		StatusError:  "openvsx returned",
	})
	if err != nil {
		return nil, err
	}

	date := dateutil.FormatDate(opts.EndDate)

	records := []Record{
		{Metric: "total_downloads", ProjectID: opts.ProjectID, Target: o.ExtensionID, Date: date, Value: result.DownloadCount},
		{Metric: "total_reviews", ProjectID: opts.ProjectID, Target: o.ExtensionID, Date: date, Value: result.ReviewCount},
	}

	if result.AverageRating != nil {
		records = append(records, Record{
			Metric:    "total_ratings",
			ProjectID: opts.ProjectID,
			Target:    o.ExtensionID,
			Date:      date,
			Value:     int64(*result.AverageRating * 100),
		})
	}

	return records, nil
}

func init() {
	Register(Meta{
		Name:      "openvsx",
		Freshness: map[string]int{"": 2},
		// Download and review totals are snapshots.
		Archive:           ArchiveFull,
		NoReferenceTotals: "its totals are the data itself",
	})
}
