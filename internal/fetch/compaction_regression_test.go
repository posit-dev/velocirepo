package fetch

// End-to-end regression tests for the compaction findings in
// docs/review/2026-10-correctness-review.md. They drive the real orchestrator
// (Source → runJobs → resolveStartDate → runMetricJob → store.WriteRecords →
// store.Aggregate). All fail at deabcc6.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

// rangeSource returns one daily_downloads record per day in the requested
// range, like PyPI or CRAN.
type rangeSource struct{ target string }

func (rangeSource) Name() string { return "pypi" }

func (s rangeSource) Fetch(_ context.Context, opts source.FetchOptions) ([]source.Record, error) {
	var records []source.Record
	for d := opts.StartDate; !d.After(opts.EndDate); d = d.AddDate(0, 0, 1) {
		records = append(records, source.Record{
			Metric:    "daily_downloads",
			ProjectID: opts.ProjectID,
			Target:    s.target,
			Date:      d.Format("2006-01-02"),
			Value:     int64(d.YearDay()),
		})
	}
	return records, nil
}

func pypiConfig(dataDir string) *config.Config {
	return &config.Config{
		Data:     config.DataConfig{Dir: dataDir},
		Projects: map[string]config.Project{"proj": {PyPI: config.StringList{"pkg"}}},
	}
}

func storedDates(t *testing.T, dataDir string) []string {
	t.Helper()
	dir := store.MetricsProjectDir(dataDir, "pypi", "proj")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		records, err := store.ReadRecords(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range records {
			seen[r.Date] = true
		}
	}
	dates := make([]string, 0, len(seen))
	for d := range seen {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	return dates
}

func missingDates(stored []string, from, to time.Time) []string {
	have := map[string]bool{}
	for _, d := range stored {
		have[d] = true
	}
	var missing []string
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if !have[d.Format("2006-01-02")] {
			missing = append(missing, d.Format("2006-01-02"))
		}
	}
	return missing
}

// Fetching a bounded historical range (`fetch --start-date … --end-date …`)
// must leave the resume point at the end date. Fails at deabcc6: the run's
// compaction step treats January (and then all of 2025) as complete, so the
// resume point jumps to 2025-12-31 and the next fetch silently starts at
// 2026-01-01.
func TestFetchWithPastEndDateKeepsResumePoint(t *testing.T) {
	dataDir := t.TempDir()
	if _, err := Source(context.Background(), pypiConfig(dataDir), Tokens{}, "pypi",
		Options{StartDate: "2025-01-01", EndDate: "2025-01-20"},
		func(string, config.Project) []source.Source { return []source.Source{rangeSource{target: "pkg"}} },
	); err != nil {
		t.Fatal(err)
	}

	last, err := store.LastDate(dataDir, "pypi", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if want := "2025-01-20"; last.Format("2006-01-02") != want {
		t.Fatalf("resume point after fetching through %s is %s; the next fetch would skip %s..%s",
			want, last.Format("2006-01-02"), "2025-01-21", last.Format("2006-01-02"))
	}
}

// Backfilling history in two chunks must keep both chunks. Fails at deabcc6:
// the second run rebuilds 2025.jsonl from the second chunk's monthly files
// only, deleting the first chunk.
func TestFetchBackfillInChunksKeepsAllData(t *testing.T) {
	dataDir := t.TempDir()
	run := func(start, end string) {
		t.Helper()
		if _, err := Source(context.Background(), pypiConfig(dataDir), Tokens{}, "pypi",
			Options{StartDate: start, EndDate: end},
			func(string, config.Project) []source.Source { return []source.Source{rangeSource{target: "pkg"}} },
		); err != nil {
			t.Fatal(err)
		}
	}
	run("2025-01-01", "2025-01-20")
	run("2025-01-21", "2025-02-28")

	missing := missingDates(storedDates(t, dataDir), time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 2, 28, 0, 0, 0, 0, time.UTC))
	if len(missing) > 0 {
		t.Fatalf("%d of 59 backfilled days lost: %s … %s", len(missing), missing[0], missing[len(missing)-1])
	}
}

// clockSource simulates an upstream API on a simulated clock. Data for day D
// is published at D+1 00:00 UTC plus lag. Fetch fails on the run dates in
// failOn. Every fetch moves the clock on by runDuration, so the orchestrator's
// compaction step sees the time at which the run ends.
type clockSource struct {
	now         *time.Time
	firstData   time.Time
	lag         time.Duration
	failOn      map[string]bool
	runDuration time.Duration
}

func (*clockSource) Name() string { return "pypi" }

func (s *clockSource) Fetch(_ context.Context, opts source.FetchOptions) ([]source.Record, error) {
	defer func() { *s.now = s.now.Add(s.runDuration) }()
	if s.failOn[s.now.Format("2006-01-02")] {
		return nil, errors.New("simulated outage")
	}
	var records []source.Record
	for d := opts.StartDate; !d.After(opts.EndDate); d = d.AddDate(0, 0, 1) {
		if d.Before(s.firstData) || s.now.Before(d.AddDate(0, 0, 1).Add(s.lag)) {
			continue
		}
		records = append(records, source.Record{
			Metric: "daily_downloads", ProjectID: opts.ProjectID, Target: "pkg",
			Date: d.Format("2006-01-02"), Value: int64(d.YearDay()),
		})
	}
	return records, nil
}

// simulateCron runs the orchestrator once per simulated day, the way the
// generated GitHub workflow does, from first through last. Options.Now puts
// the whole run, including compaction, on the simulated clock.
func simulateCron(t *testing.T, src *clockSource, first, last time.Time, startOfRun time.Duration) string {
	t.Helper()
	dataDir := t.TempDir()
	var now time.Time
	src.now = &now
	for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
		now = day.Add(startOfRun)
		if _, err := Source(context.Background(), pypiConfig(dataDir), Tokens{}, "pypi",
			Options{Now: func() time.Time { return now }},
			func(string, config.Project) []source.Source { return []source.Source{src} },
		); err != nil {
			t.Fatal(err)
		}
	}
	return dataDir
}

// Daily runs across 13 month boundaries and one year boundary must end up
// with every day stored. Fails at deabcc6 in all three scenarios: whenever
// the last day of a month has not been stored when the month is compacted, it
// is never fetched.
func TestSimulatedDailyCronStoresEveryDay(t *testing.T) {
	first := time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC)
	last := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	firstData := time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC)
	// The final runs may not have the newest days yet; check up to 3 days back.
	checkThrough := last.AddDate(0, 0, -3)

	tests := []struct {
		name       string
		src        clockSource
		startOfRun time.Duration
	}{
		{
			name:       "outage on the first of a month",
			src:        clockSource{failOn: map[string]bool{"2025-03-01": true, "2026-01-01": true}, runDuration: time.Minute},
			startOfRun: time.Hour, // the default "0 1 * * *" cron
		},
		{
			name:       "source publishes a day late",
			src:        clockSource{lag: 6 * time.Hour, runDuration: time.Minute}, // yesterday is not available at 01:00 UTC
			startOfRun: time.Hour,
		},
		{
			name:       "run crosses midnight UTC",
			src:        clockSource{runDuration: 10 * time.Minute},
			startOfRun: 23*time.Hour + 55*time.Minute,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := tt.src
			src.firstData = firstData
			dataDir := simulateCron(t, &src, first, last, tt.startOfRun)
			if missing := missingDates(storedDates(t, dataDir), firstData, checkThrough); len(missing) > 0 {
				t.Errorf("%d days never stored: %s", len(missing), strings.Join(missing, ", "))
			}
		})
	}
}

// Sanity check for the harness: with no outage, no lag and a run that does
// not cross midnight, every day is stored. Passes at deabcc6.
func TestSimulatedDailyCronHarness(t *testing.T) {
	first := time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC)
	last := time.Date(2025, 3, 10, 0, 0, 0, 0, time.UTC)
	src := clockSource{firstData: time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC), runDuration: time.Minute}
	dataDir := simulateCron(t, &src, first, last, time.Hour)
	if missing := missingDates(storedDates(t, dataDir), src.firstData, last.AddDate(0, 0, -1)); len(missing) > 0 {
		t.Fatalf("%d days never stored: %s", len(missing), fmt.Sprint(missing))
	}
}
