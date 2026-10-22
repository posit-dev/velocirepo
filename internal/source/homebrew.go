package source

import (
	"context"
	"fmt"
	"net/http"

	"github.com/posit-dev/velocirepo/internal/dateutil"
)

type Homebrew struct {
	Client  *http.Client
	Formula string
	BaseURL string // override for testing
}

func (h *Homebrew) Name() string { return "homebrew" }

func (h *Homebrew) baseURL() string {
	if h.BaseURL != "" {
		return h.BaseURL
	}
	return "https://formulae.brew.sh"
}

func (h *Homebrew) Fetch(ctx context.Context, opts FetchOptions) ([]Record, error) {
	url := fmt.Sprintf("%s/api/formula/%s.json", h.baseURL(), h.Formula)

	result, err := doJSON[struct {
		Analytics struct {
			Install map[string]map[string]int64 `json:"install"`
		} `json:"analytics"`
	}](ctx, h.Client, httpJSONRequest{
		URL:          url,
		RequestError: "request homebrew",
		StatusError:  "homebrew API returned",
	})
	if err != nil {
		return nil, err
	}

	date := dateutil.FormatDate(opts.EndDate)
	var records []Record

	for period, variants := range result.Analytics.Install {
		metric := "downloads_" + period
		var total int64
		for _, count := range variants {
			total += count
		}
		records = append(records, Record{
			Metric:    metric,
			ProjectID: opts.ProjectID,
			Target:    h.Formula,
			Date:      date,
			Value:     total,
		})
	}

	return records, nil
}

func init() {
	Register(Meta{
		Name:      "homebrew",
		Freshness: map[string]int{"": 2},
		// Install counts are snapshots.
		Archive:           ArchiveFull,
		NoReferenceTotals: "its install counts are the data itself",
		Target:            "a Homebrew formula name",
		Metrics: []MetricInfo{
			{Name: "downloads_30d", Kind: MetricWindow, Description: "installs in the 30 days before the fetch"},
			{Name: "downloads_90d", Kind: MetricWindow, Description: "installs in the 90 days before the fetch"},
			{Name: "downloads_365d", Kind: MetricWindow, Description: "installs in the 365 days before the fetch"},
		},
		Notes: "Homebrew reports installs over rolling windows, stored on every day they're fetched. Read the latest value; never sum them over days.",
	})
}
