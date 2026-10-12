package store

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/dateutil"
	"github.com/posit-dev/velocirepo/internal/source"
)

// storeLaterRecord stores a record for a day after the period under test. A
// period is only compacted once the project has data through its last day, so
// tests about compacting a period store something after it.
func storeLaterRecord(t *testing.T, dir, sourceName, projectID, date string) {
	t.Helper()
	writeTestRecords(t, metricsPath(dir, sourceName, projectID, date),
		source.Record{Source: sourceName, Metric: "daily_downloads", ProjectID: projectID, Date: date, Value: 1})
}

func TestAggregateDailyToMonthly(t *testing.T) {
	dir := t.TempDir()

	// Create 3 daily files for January 2025
	for day := 1; day <= 3; day++ {
		date := time.Date(2025, 1, day, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		records := []source.Record{
			{Metric: "downloads", ProjectID: "mylib", Date: date, Value: int64(day * 100)},
		}
		if _, err := WriteRecords(dir, "pypi", "mylib", records); err != nil {
			t.Fatal(err)
		}
	}
	storeLaterRecord(t, dir, "pypi", "mylib", "2025-02-01")

	// Run aggregate with "now" being Feb 2025 (January is complete)
	now := time.Date(2025, 2, 5, 0, 0, 0, 0, time.UTC)
	if err := Aggregate(dir, now); err != nil {
		t.Fatalf("Aggregate failed: %v", err)
	}

	// Monthly file should exist
	monthlyPath := metricsPath(dir, "pypi", "mylib", "2025-01")
	if _, err := os.Stat(monthlyPath); err != nil {
		t.Fatalf("monthly file not created: %s", monthlyPath)
	}

	// Daily files should be deleted
	for day := 1; day <= 3; day++ {
		date := time.Date(2025, 1, day, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		dailyPath := metricsPath(dir, "pypi", "mylib", date)
		if _, err := os.Stat(dailyPath); !os.IsNotExist(err) {
			t.Errorf("daily file not deleted: %s", dailyPath)
		}
	}

	// Read monthly file and verify contents
	records, err := ReadRecords(monthlyPath)
	if err != nil {
		t.Fatalf("ReadRecords failed: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("got %d records, want 3", len(records))
	}
	for i, r := range records {
		wantDate := time.Date(2025, 1, i+1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		if r.Date != wantDate || r.Value != int64((i+1)*100) {
			t.Errorf("record %d = %s/%d, want %s/%d", i, r.Date, r.Value, wantDate, (i+1)*100)
		}
	}

	// February is not complete yet, so its daily file stays.
	if _, err := os.Stat(metricsPath(dir, "pypi", "mylib", "2025-02-01")); err != nil {
		t.Errorf("February daily file should still exist: %v", err)
	}
}

func TestAggregateSkipsIncompleteMonth(t *testing.T) {
	dir := t.TempDir()

	// Create daily files for June 2025
	records := []source.Record{
		{Metric: "downloads", ProjectID: "mylib", Date: "2025-06-01", Value: 100},
	}
	if _, err := WriteRecords(dir, "pypi", "mylib", records); err != nil {
		t.Fatal(err)
	}

	// Run aggregate with "now" being June 15 (month not complete)
	now := time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC)
	if err := Aggregate(dir, now); err != nil {
		t.Fatalf("Aggregate failed: %v", err)
	}

	// Monthly file should NOT exist
	monthlyPath := metricsPath(dir, "pypi", "mylib", "2025-06")
	if _, err := os.Stat(monthlyPath); !os.IsNotExist(err) {
		t.Error("monthly file should not be created for incomplete month")
	}

	// Daily file should still exist
	dailyPath := metricsPath(dir, "pypi", "mylib", "2025-06-01")
	if _, err := os.Stat(dailyPath); err != nil {
		t.Errorf("daily file should still exist: %s", dailyPath)
	}
}

func TestAggregateMonthlyToYearly(t *testing.T) {
	dir := t.TempDir()

	// Create monthly files for 2024
	for month := 1; month <= 3; month++ {
		date := time.Date(2024, time.Month(month), 15, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		records := []source.Record{
			{Metric: "downloads", ProjectID: "pkg", Date: date, Value: int64(month * 1000)},
		}
		monthStr := time.Date(2024, time.Month(month), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		writeTestRecords(t, metricsPath(dir, "cran", "pkg", monthStr), records...)
	}
	storeLaterRecord(t, dir, "cran", "pkg", "2025-01-15")

	// Run aggregate with "now" being 2025 (2024 is complete)
	now := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := Aggregate(dir, now); err != nil {
		t.Fatalf("Aggregate failed: %v", err)
	}

	// Yearly file should exist
	yearlyPath := metricsPath(dir, "cran", "pkg", "2024")
	if _, err := os.Stat(yearlyPath); err != nil {
		t.Fatalf("yearly file not created: %s", yearlyPath)
	}

	// Monthly files should be deleted
	for month := 1; month <= 3; month++ {
		monthStr := time.Date(2024, time.Month(month), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		monthlyPath := metricsPath(dir, "cran", "pkg", monthStr)
		if _, err := os.Stat(monthlyPath); !os.IsNotExist(err) {
			t.Errorf("monthly file not deleted: %s", monthlyPath)
		}
	}

	records, err := ReadRecords(yearlyPath)
	if err != nil {
		t.Fatalf("ReadRecords failed: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("got %d records, want 3", len(records))
	}
	for i, r := range records {
		wantDate := time.Date(2024, time.Month(i+1), 15, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		if r.Date != wantDate || r.Value != int64((i+1)*1000) {
			t.Errorf("record %d = %s/%d, want %s/%d", i, r.Date, r.Value, wantDate, (i+1)*1000)
		}
	}
}

func TestAggregateDedup(t *testing.T) {
	dir := t.TempDir()

	// Write duplicate records across two daily files
	r1 := []source.Record{
		{Metric: "downloads", ProjectID: "mylib", Date: "2025-01-01", Value: 100},
	}
	r2 := []source.Record{
		{Metric: "downloads", ProjectID: "mylib", Date: "2025-01-01", Value: 100},
		{Metric: "downloads", ProjectID: "mylib", Date: "2025-01-02", Value: 200},
	}

	writeTestRecords(t, metricsPath(dir, "pypi", "mylib", "2025-01-01"), r1...)
	writeTestRecords(t, metricsPath(dir, "pypi", "mylib", "2025-01-02"), r2...)
	storeLaterRecord(t, dir, "pypi", "mylib", "2025-02-01")

	now := time.Date(2025, 2, 5, 0, 0, 0, 0, time.UTC)
	if err := Aggregate(dir, now); err != nil {
		t.Fatalf("Aggregate failed: %v", err)
	}

	monthlyPath := metricsPath(dir, "pypi", "mylib", "2025-01")
	records, err := ReadRecords(monthlyPath)
	if err != nil {
		t.Fatalf("ReadRecords failed: %v", err)
	}

	// Should have 2 unique records, not 3
	if len(records) != 2 {
		t.Errorf("got %d records after dedup, want 2", len(records))
	}
}

func TestAggregateDedupWithTags(t *testing.T) {
	dir := t.TempDir()

	records := []source.Record{
		{Metric: "views", ProjectID: "proj", Date: "2025-01-01", Value: 100, Extra: map[string]string{"video_id": "a"}},
		{Metric: "views", ProjectID: "proj", Date: "2025-01-01", Value: 200, Extra: map[string]string{"video_id": "b"}},
		{Metric: "views", ProjectID: "proj", Date: "2025-01-01", Value: 100, Extra: map[string]string{"video_id": "a"}},
	}

	writeTestRecords(t, metricsPath(dir, "youtube", "proj", "2025-01-01"), records...)
	storeLaterRecord(t, dir, "youtube", "proj", "2025-02-01")

	now := time.Date(2025, 2, 5, 0, 0, 0, 0, time.UTC)
	if err := Aggregate(dir, now); err != nil {
		t.Fatalf("Aggregate failed: %v", err)
	}

	monthlyPath := metricsPath(dir, "youtube", "proj", "2025-01")
	got, err := ReadRecords(monthlyPath)
	if err != nil {
		t.Fatalf("ReadRecords failed: %v", err)
	}

	// video_id=a and video_id=b are distinct, duplicate video_id=a is removed
	if len(got) != 2 {
		t.Errorf("got %d records after dedup, want 2", len(got))
	}
}

func TestAggregateDedupPreservesMetricTargets(t *testing.T) {
	dir := t.TempDir()
	records := []source.Record{
		{Source: "rss", Metric: "total_items", ProjectID: "site", Target: "https://example.com/blog.xml", Date: "2025-01-01", Value: 100},
		{Source: "rss", Metric: "total_items", ProjectID: "site", Target: "https://example.com/events.xml", Date: "2025-01-01", Value: 20},
	}
	writeTestRecords(t, metricsPath(dir, "rss", "site", "2025-01-01"), records...)
	storeLaterRecord(t, dir, "rss", "site", "2025-02-01")

	if err := Aggregate(dir, time.Date(2025, 2, 5, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Aggregate failed: %v", err)
	}

	got, err := ReadRecords(metricsPath(dir, "rss", "site", "2025-01"))
	if err != nil {
		t.Fatalf("ReadRecords failed: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d records after dedup, want 2", len(got))
	}
}

func TestAggregateDedupPreservesEventTargetsAndRefs(t *testing.T) {
	dir := t.TempDir()
	firstRef := 101
	secondRef := 102
	events := []source.Event{
		{Source: "github", Type: "issue_close", ProjectID: "pkg", Target: "org/pkg", Datetime: "2025-01-01T12:00:00Z", Ref: &firstRef, User: "maintainer"},
		{Source: "github", Type: "issue_close", ProjectID: "pkg", Target: "org/pkg", Datetime: "2025-01-01T12:00:00Z", Ref: &secondRef, User: "maintainer"},
		{Source: "github", Type: "issue_close", ProjectID: "pkg", Target: "org/other", Datetime: "2025-01-01T12:00:00Z", Ref: &firstRef, User: "maintainer"},
	}
	writeTestEvents(t, eventsPath(dir, "github", "pkg", "2025-01-01"), events...)
	writeTestEvents(t, eventsPath(dir, "github", "pkg", "2025-02-01"),
		source.Event{Source: "github", Type: "star", ProjectID: "pkg", Target: "org/pkg", Datetime: "2025-02-01T09:00:00Z", User: "later"})

	if err := Aggregate(dir, time.Date(2025, 2, 5, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Aggregate failed: %v", err)
	}

	got, err := ReadEvents(eventsPath(dir, "github", "pkg", "2025-01"))
	if err != nil {
		t.Fatalf("ReadEvents failed: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d events after dedup, want 3", len(got))
	}
}

// A re-fetched day corrects the value stored in an already-compacted month:
// the newer daily file wins over the existing monthly file.
func TestAggregateNewerFileWins(t *testing.T) {
	dir := t.TempDir()
	writeTestRecords(t, metricsPath(dir, "pypi", "mylib", "2025-01"),
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "mylib", Target: "mylib", Date: "2025-01-15", Value: 100},
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "mylib", Target: "mylib", Date: "2025-01-16", Value: 160},
	)
	if _, err := WriteRecords(dir, "pypi", "mylib", []source.Record{
		{Metric: "daily_downloads", ProjectID: "mylib", Target: "mylib", Date: "2025-01-15", Value: 150},
	}); err != nil {
		t.Fatal(err)
	}

	if err := Aggregate(dir, time.Date(2025, 2, 2, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Aggregate failed: %v", err)
	}

	got, err := ReadRecords(metricsPath(dir, "pypi", "mylib", "2025-01"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Date != "2025-01-15" || got[0].Value != 150 || got[1].Date != "2025-01-16" || got[1].Value != 160 {
		t.Fatalf("got %+v, want 2025-01-15=150 (re-fetched) and 2025-01-16=160", got)
	}
}

// A total_* series only stores a record when its value changes, and the
// watermark records the later days that were checked. A month whose last
// change came early still compacts once the watermark passes its end.
func TestAggregateCountsWatermarkAsStored(t *testing.T) {
	dir := t.TempDir()
	for d := time.Date(2025, 1, 5, 0, 0, 0, 0, time.UTC); !d.After(time.Date(2025, 2, 2, 0, 0, 0, 0, time.UTC)); d = d.AddDate(0, 0, 1) {
		if _, err := WriteRecords(dir, "openvsx", "proj", []source.Record{
			{Metric: "total_downloads", ProjectID: "proj", Target: "ext", Date: d.Format("2006-01-02"), Value: 10},
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := Aggregate(dir, time.Date(2025, 2, 3, 1, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	got, err := ReadRecords(metricsPath(dir, "openvsx", "proj", "2025-01"))
	if err != nil {
		t.Fatalf("January was not compacted: %v", err)
	}
	if len(got) != 1 || got[0].Date != "2025-01-05" || got[0].Value != 10 {
		t.Fatalf("got %+v, want the single change point on 2025-01-05", got)
	}
	last, err := LastDate(dir, "openvsx", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if last.Format("2006-01-02") != "2025-02-02" {
		t.Errorf("LastDate = %s, want 2025-02-02 (the watermark)", last.Format("2006-01-02"))
	}
}

// Property test: whatever the sequence of fetches, re-fetches of past days and
// compaction runs, compaction never loses a record, keeps the newest value of
// each record, never moves the resume point, and eventually compacts every
// period whose last day has been stored.
func TestAggregatePreservesDataAndResumePoint(t *testing.T) {
	targets := []string{"a", "b", "c"}
	first := time.Date(2023, 11, 20, 0, 0, 0, 0, time.UTC)

	for seed := int64(1); seed <= 20; seed++ {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			dir := t.TempDir()
			want := map[string]int64{}
			fetched := first.AddDate(0, 0, -1)
			now := first

			write := func(from, to time.Time) {
				var batch []source.Record
				for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
					for _, target := range targets {
						v := rng.Int63n(1000)
						date := d.Format("2006-01-02")
						batch = append(batch, source.Record{Metric: "daily_downloads", ProjectID: "proj", Target: target, Date: date, Value: v})
						want[target+"|"+date] = v
					}
				}
				if _, err := WriteRecords(dir, "pypi", "proj", batch); err != nil {
					t.Fatal(err)
				}
			}

			for step := 0; step < 120; step++ {
				switch op := rng.Intn(10); {
				case op < 6: // the next fetch
					from := fetched.AddDate(0, 0, 1)
					fetched = from.AddDate(0, 0, rng.Intn(10))
					write(from, fetched)
				case op < 8: // a backfill or correction of a past day
					if fetched.Before(first) {
						continue
					}
					day := first.AddDate(0, 0, rng.Intn(int(fetched.Sub(first).Hours()/24)+1))
					write(day, day)
				default: // a compaction run, some time after the latest data
					if earliest := fetched.AddDate(0, 0, 1); now.Before(earliest) {
						now = earliest
					}
					now = now.Add(time.Duration(rng.Intn(72)) * time.Hour)
					aggregateAndCheck(t, dir, now, want)
				}
			}

			// Long after the last fetch, every period up to the last stored day
			// is compacted, and only the partially stored month stays daily.
			aggregateAndCheck(t, dir, fetched.AddDate(2, 0, 0), want)
			last := fetched.Format("2006-01-02")
			for _, name := range projectFileNames(t, MetricsProjectDir(dir, "pypi", "proj")) {
				period := strings.TrimSuffix(name, ".jsonl")
				switch len(period) {
				case len("2006-01-02"):
					if dateutil.LastDayOfMonth(period[:7]) <= last {
						t.Errorf("%s was not compacted although its month is stored through %s", name, last)
					}
				case len("2006-01"):
					if period[:4]+"-12-31" <= last {
						t.Errorf("%s was not compacted although its year is stored through %s", name, last)
					}
				}
			}
		})
	}
}

func aggregateAndCheck(t *testing.T, dir string, now time.Time, want map[string]int64) {
	t.Helper()
	before, err := LastDate(dir, "pypi", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if err := Aggregate(dir, now); err != nil {
		t.Fatal(err)
	}
	after, err := LastDate(dir, "pypi", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if !after.Equal(before) {
		t.Fatalf("compaction at %s moved the resume point from %s to %s", now.Format(time.RFC3339), before.Format("2006-01-02"), after.Format("2006-01-02"))
	}

	got := readProjectRecords(t, dir, "pypi", "proj")
	if len(got) != len(want) {
		t.Fatalf("compaction at %s: %d records stored, want %d", now.Format(time.RFC3339), len(got), len(want))
	}
	for _, r := range got {
		key := r.Target + "|" + r.Date
		if v, ok := want[key]; !ok || v != r.Value {
			t.Fatalf("compaction at %s: %s = %d, want %d (present: %v)", now.Format(time.RFC3339), key, r.Value, v, ok)
		}
	}
}

// A project whose files can't be read is reported, and doesn't stop the other
// projects from being compacted.
func TestAggregateReportsErrorsAndContinues(t *testing.T) {
	dir := t.TempDir()
	writeTestRaw(t, metricsPath(dir, "pypi", "broken", "2025-01-01"), []string{`{not json`})
	storeLaterRecord(t, dir, "pypi", "broken", "2025-02-01")
	if _, err := WriteRecords(dir, "pypi", "fine", []source.Record{
		{Metric: "daily_downloads", ProjectID: "fine", Date: "2025-01-15", Value: 1},
		{Metric: "daily_downloads", ProjectID: "fine", Date: "2025-02-01", Value: 2},
	}); err != nil {
		t.Fatal(err)
	}

	err := Aggregate(dir, time.Date(2025, 2, 5, 0, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), filepath.Join("pypi", "broken")) {
		t.Fatalf("Aggregate error = %v, want an error naming the broken project", err)
	}
	if _, err := os.Stat(metricsPath(dir, "pypi", "fine", "2025-01")); err != nil {
		t.Errorf("the healthy project was not compacted: %v", err)
	}
	if _, err := os.Stat(metricsPath(dir, "pypi", "broken", "2025-01-01")); err != nil {
		t.Errorf("the broken project's daily file should be left alone: %v", err)
	}
}
