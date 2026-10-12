package fetch

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/source"
)

// endDateSource records the end date the orchestrator asks it for.
type endDateSource struct {
	name string
	got  *time.Time
}

func (s endDateSource) Name() string { return s.name }

func (s endDateSource) Fetch(_ context.Context, opts source.FetchOptions) ([]source.Record, error) {
	*s.got = opts.EndDate
	return nil, nil
}

func TestFetchRejectsStartDateAfterEndDate(t *testing.T) {
	_, err := Source(context.Background(), pypiConfig(t.TempDir()), Tokens{}, "pypi",
		Options{StartDate: "2025-02-10", EndDate: "2025-02-01"},
		func(string, config.Project) []source.Source { return []source.Source{rangeSource{target: "pkg"}} },
	)
	if err == nil || !strings.Contains(err.Error(), "after end date") {
		t.Fatalf("err = %v, want start date after end date", err)
	}
}

// Plausible reports days in the site's time zone, so its default end date is
// one day earlier than other sources'. An explicit end date is respected.
func TestDefaultEndDateSettlesPlausible(t *testing.T) {
	now := time.Date(2025, 3, 10, 1, 0, 0, 0, time.UTC)
	tests := []struct {
		sourceName string
		endDate    string
		want       string
	}{
		{"plausible", "", "2025-03-08"},
		{"pypi", "", "2025-03-09"},
		{"plausible", "2025-03-09", "2025-03-09"},
	}
	for _, tt := range tests {
		var got time.Time
		cfg := &config.Config{Data: config.DataConfig{Dir: t.TempDir()}, Projects: map[string]config.Project{"proj": {}}}
		if _, err := Source(context.Background(), cfg, Tokens{}, tt.sourceName,
			Options{EndDate: tt.endDate, StartDate: "2025-03-01", Now: func() time.Time { return now }},
			func(string, config.Project) []source.Source {
				return []source.Source{endDateSource{name: tt.sourceName, got: &got}}
			},
		); err != nil {
			t.Fatal(err)
		}
		if got.Format("2006-01-02") != tt.want {
			t.Errorf("%s with end date %q fetched through %s, want %s", tt.sourceName, tt.endDate, got.Format("2006-01-02"), tt.want)
		}
	}
}

func TestTrafficGapIsLogged(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	now := time.Date(2025, 3, 20, 1, 0, 0, 0, time.UTC)
	dataDir := t.TempDir()
	if _, err := resolveStartDate(dataDir, "github-traffic", "proj", "", now); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Errorf("first fetch logged a gap: %s", logs.String())
	}

	if _, err := Source(context.Background(), &config.Config{Data: config.DataConfig{Dir: dataDir}, Projects: map[string]config.Project{"proj": {}}}, Tokens{}, "github-traffic",
		Options{StartDate: "2025-02-01", EndDate: "2025-02-01", NoConcatenate: true},
		func(string, config.Project) []source.Source { return []source.Source{trafficDay{}} },
	); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveStartDate(dataDir, "github-traffic", "proj", "", now); err != nil {
		t.Fatal(err)
	}
	if out := logs.String(); !strings.Contains(out, "14 days") || !strings.Contains(out, "from=2025-02-02") || !strings.Contains(out, "to=2025-03-05") {
		t.Errorf("log = %q, want a warning for 2025-02-02..2025-03-05", out)
	}
}

// trafficDay returns one traffic record for the start date.
type trafficDay struct{}

func (trafficDay) Name() string { return "github-traffic" }

func (trafficDay) Fetch(_ context.Context, opts source.FetchOptions) ([]source.Record, error) {
	return []source.Record{{Metric: "daily_views", ProjectID: opts.ProjectID, Target: "org/repo", Date: opts.StartDate.Format("2006-01-02"), Value: 1}}, nil
}
