package store

// Regression tests for the compaction findings in
// docs/review/2026-10-correctness-review.md. The tests marked "fails at
// deabcc6" reproduce a bug and describe the behaviour compaction should have;
// the others pin down behaviour that is correct today and must survive a fix.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/source"
)

// readProjectRecords returns every record stored for a metrics project,
// whether it lives in a daily, monthly, or yearly file.
func readProjectRecords(t *testing.T, dataDir, sourceName, projectID string) []source.Record {
	t.Helper()
	dir := MetricsProjectDir(dataDir, sourceName, projectID)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var all []source.Record
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		records, err := ReadRecords(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, records...)
	}
	return all
}

func readProjectEvents(t *testing.T, dataDir, sourceName, projectID string) []source.Event {
	t.Helper()
	dir := EventsProjectDir(dataDir, sourceName, projectID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var all []source.Event
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		events, err := ReadEvents(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, events...)
	}
	return all
}

func projectFileNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

func dailyRecords(target string, from, to time.Time) []source.Record {
	var records []source.Record
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		records = append(records, source.Record{
			Metric:    "daily_downloads",
			ProjectID: "proj",
			Target:    target,
			Date:      d.Format("2006-01-02"),
			Value:     int64(d.YearDay()),
		})
	}
	return records
}

func utcDate(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// A daily file that lands in an already-compacted month (e.g. after
// `fetch --start-date` to backfill a newly added target) must be merged into
// the monthly file, not replace it. Fails at deabcc6: the monthly file is
// rebuilt from the daily file alone and the other 31 records are lost.
func TestAggregateMergesLateDailyFileIntoExistingMonth(t *testing.T) {
	dir := t.TempDir()

	if _, err := WriteRecords(dir, "pypi", "proj", dailyRecords("pkg-a", utcDate(2025, 1, 1), utcDate(2025, 1, 31))); err != nil {
		t.Fatal(err)
	}
	if err := Aggregate(dir, utcDate(2025, 2, 2)); err != nil {
		t.Fatal(err)
	}
	if got := len(readProjectRecords(t, dir, "pypi", "proj")); got != 31 {
		t.Fatalf("setup: %d records after first compaction, want 31", got)
	}

	if _, err := WriteRecords(dir, "pypi", "proj", dailyRecords("pkg-b", utcDate(2025, 1, 31), utcDate(2025, 1, 31))); err != nil {
		t.Fatal(err)
	}
	if err := Aggregate(dir, utcDate(2025, 2, 3)); err != nil {
		t.Fatal(err)
	}

	got := readProjectRecords(t, dir, "pypi", "proj")
	if len(got) != 32 {
		t.Fatalf("after compacting a late daily file: %d records, want 32 (31 for pkg-a + 1 for pkg-b); files: %v",
			len(got), projectFileNames(t, MetricsProjectDir(dir, "pypi", "proj")))
	}
}

// Same as above one level up: a late daily file in an already-compacted year
// becomes a monthly file, and that monthly file must be merged into the yearly
// file. Fails at deabcc6: the whole year is replaced by the single late day.
func TestAggregateMergesLateDataIntoExistingYear(t *testing.T) {
	dir := t.TempDir()

	if _, err := WriteRecords(dir, "pypi", "proj", dailyRecords("pkg-a", utcDate(2024, 1, 1), utcDate(2024, 12, 31))); err != nil {
		t.Fatal(err)
	}
	if err := Aggregate(dir, utcDate(2025, 1, 2)); err != nil {
		t.Fatal(err)
	}
	if names := projectFileNames(t, MetricsProjectDir(dir, "pypi", "proj")); len(names) != 1 || names[0] != "2024.jsonl" {
		t.Fatalf("setup: files after first compaction = %v, want [2024.jsonl]", names)
	}

	if _, err := WriteRecords(dir, "pypi", "proj", dailyRecords("pkg-b", utcDate(2024, 12, 31), utcDate(2024, 12, 31))); err != nil {
		t.Fatal(err)
	}
	if err := Aggregate(dir, utcDate(2025, 1, 3)); err != nil {
		t.Fatal(err)
	}

	got := readProjectRecords(t, dir, "pypi", "proj")
	if len(got) != 367 {
		t.Fatalf("after compacting late data into 2024: %d records, want 367 (366 for pkg-a + 1 for pkg-b)", len(got))
	}
}

// Events go through the same compaction code. Fails at deabcc6.
func TestAggregateMergesLateEventsIntoExistingMonth(t *testing.T) {
	dir := t.TempDir()

	var january []source.Event
	for day := 1; day <= 31; day++ {
		january = append(january, source.Event{
			Type: "star", ProjectID: "proj", Target: "org/repo",
			Datetime: fmt.Sprintf("2025-01-%02dT12:00:00Z", day), User: fmt.Sprintf("user%d", day),
		})
	}
	if _, err := WriteEvents(dir, "github", "proj", january); err != nil {
		t.Fatal(err)
	}
	if err := Aggregate(dir, utcDate(2025, 2, 2)); err != nil {
		t.Fatal(err)
	}

	late := []source.Event{{Type: "star", ProjectID: "proj", Target: "org/other", Datetime: "2025-01-15T08:00:00Z", User: "late"}}
	if _, err := WriteEvents(dir, "github", "proj", late); err != nil {
		t.Fatal(err)
	}
	if err := Aggregate(dir, utcDate(2025, 2, 3)); err != nil {
		t.Fatal(err)
	}

	if got := len(readProjectEvents(t, dir, "github", "proj")); got != 32 {
		t.Fatalf("after compacting a late events file: %d events, want 32", got)
	}
}

// Compaction only changes the file layout, so it must not change the
// resume point that the next fetch starts from. Fails at deabcc6: a monthly
// file is read back as "fetched through the last day of the month", so a day
// that was never fetched (failed run, source lag) is silently skipped forever.
func TestAggregateDoesNotAdvanceResumePoint(t *testing.T) {
	tests := []struct {
		name     string
		lastDay  time.Time // last day that was actually fetched
		firstDay time.Time
		now      time.Time // compaction time (the 01:00 UTC cron run)
	}{
		{
			name:     "month end",
			firstDay: utcDate(2025, 1, 1),
			lastDay:  utcDate(2025, 1, 30),
			now:      time.Date(2025, 2, 1, 1, 0, 0, 0, time.UTC),
		},
		{
			name:     "leap-year February",
			firstDay: utcDate(2024, 2, 1),
			lastDay:  utcDate(2024, 2, 28),
			now:      time.Date(2024, 3, 1, 1, 0, 0, 0, time.UTC),
		},
		{
			name:     "year end",
			firstDay: utcDate(2024, 1, 1),
			lastDay:  utcDate(2024, 12, 30),
			now:      time.Date(2025, 1, 1, 1, 0, 0, 0, time.UTC),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := WriteRecords(dir, "pypi", "proj", dailyRecords("pkg", tt.firstDay, tt.lastDay)); err != nil {
				t.Fatal(err)
			}

			before, err := LastDate(dir, "pypi", "proj")
			if err != nil {
				t.Fatal(err)
			}
			if !before.Equal(tt.lastDay) {
				t.Fatalf("setup: LastDate before compaction = %s, want %s", before.Format("2006-01-02"), tt.lastDay.Format("2006-01-02"))
			}

			if err := Aggregate(dir, tt.now); err != nil {
				t.Fatal(err)
			}

			after, err := LastDate(dir, "pypi", "proj")
			if err != nil {
				t.Fatal(err)
			}
			if !after.Equal(before) {
				t.Errorf("LastDate changed from %s to %s by compaction; the next fetch would skip %s",
					before.Format("2006-01-02"), after.Format("2006-01-02"), tt.lastDay.AddDate(0, 0, 1).Format("2006-01-02"))
			}
		})
	}
}

// A crash between writing the monthly file and deleting the daily files
// leaves both behind. The next run must converge to the same single monthly
// file without duplicates. Passes at deabcc6; guards a merge-based fix.
func TestAggregateRecoversFromCrashBetweenWriteAndDelete(t *testing.T) {
	dir := t.TempDir()
	records := dailyRecords("pkg", utcDate(2025, 1, 1), utcDate(2025, 1, 31))
	if _, err := WriteRecords(dir, "pypi", "proj", records); err != nil {
		t.Fatal(err)
	}
	// Simulate the crash: the monthly file exists, the daily files remain.
	writeTestRecords(t, metricsPath(dir, "pypi", "proj", "2025-01"), records...)

	if err := Aggregate(dir, utcDate(2025, 2, 2)); err != nil {
		t.Fatal(err)
	}

	if names := projectFileNames(t, MetricsProjectDir(dir, "pypi", "proj")); len(names) != 1 || names[0] != "2025-01.jsonl" {
		t.Fatalf("files = %v, want [2025-01.jsonl]", names)
	}
	got, err := ReadRecords(metricsPath(dir, "pypi", "proj", "2025-01"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 31 {
		t.Fatalf("got %d records, want 31", len(got))
	}
	for i, r := range got {
		if r.Date != records[i].Date || r.Value != records[i].Value {
			t.Fatalf("record %d = %s/%d, want %s/%d", i, r.Date, r.Value, records[i].Date, records[i].Value)
		}
	}
}

// Running compaction again must be a no-op. Passes at deabcc6.
func TestAggregateIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteRecords(dir, "pypi", "proj", dailyRecords("pkg", utcDate(2024, 11, 15), utcDate(2025, 2, 10))); err != nil {
		t.Fatal(err)
	}
	now := utcDate(2025, 2, 11)
	if err := Aggregate(dir, now); err != nil {
		t.Fatal(err)
	}
	projDir := MetricsProjectDir(dir, "pypi", "proj")
	snapshot := func() map[string]string {
		out := map[string]string{}
		for _, name := range projectFileNames(t, projDir) {
			data, err := os.ReadFile(filepath.Join(projDir, name))
			if err != nil {
				t.Fatal(err)
			}
			out[name] = string(data)
		}
		return out
	}
	first := snapshot()
	if err := Aggregate(dir, now); err != nil {
		t.Fatal(err)
	}
	second := snapshot()
	if len(first) != len(second) {
		t.Fatalf("file sets differ: %v vs %v", first, second)
	}
	for name, content := range first {
		if second[name] != content {
			t.Errorf("%s changed on second compaction", name)
		}
	}
	// 2024 is complete: Nov 15–Dec 31 end up in 2024.jsonl. January 2025 is
	// complete and February is not.
	want := []string{"2024.jsonl", "2025-01.jsonl", "2025-02-01.jsonl"}
	if names := projectFileNames(t, projDir); len(names) != 12 || names[0] != want[0] || names[1] != want[1] || names[2] != want[2] {
		t.Errorf("files = %v, want 2024.jsonl, 2025-01.jsonl and ten February daily files", names)
	}
}

// Period boundaries are exclusive of the first instant of the next period.
// Passes at deabcc6; the existing tests only probe mid-month instants.
func TestPeriodCompletenessBoundaries(t *testing.T) {
	tests := []struct {
		period string
		now    time.Time
		want   bool
	}{
		{"2025-01", time.Date(2025, 1, 31, 23, 59, 59, 0, time.UTC), false},
		{"2025-01", time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC), false},
		{"2025-01", time.Date(2025, 2, 1, 0, 0, 0, 1, time.UTC), true},
		{"2024-02", time.Date(2024, 2, 29, 23, 0, 0, 0, time.UTC), false},
		{"2024-02", time.Date(2024, 3, 1, 0, 0, 1, 0, time.UTC), true},
		{"2024-12", time.Date(2025, 1, 1, 0, 0, 1, 0, time.UTC), true},
		{"2025-13", time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), false},
		// A non-UTC clock is compared as an instant, not as a wall-clock date.
		{"2025-01", time.Date(2025, 1, 31, 20, 0, 0, 0, time.FixedZone("EST", -5*3600)), true},
	}
	for _, tt := range tests {
		if got := isMonthComplete(tt.period, tt.now); got != tt.want {
			t.Errorf("isMonthComplete(%q, %s) = %v, want %v", tt.period, tt.now.Format(time.RFC3339Nano), got, tt.want)
		}
	}

	yearTests := []struct {
		period string
		now    time.Time
		want   bool
	}{
		{"2024", time.Date(2024, 12, 31, 23, 59, 59, 0, time.UTC), false},
		{"2024", time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"2024", time.Date(2025, 1, 1, 0, 0, 1, 0, time.UTC), true},
	}
	for _, tt := range yearTests {
		if got := isYearComplete(tt.period, tt.now); got != tt.want {
			t.Errorf("isYearComplete(%q, %s) = %v, want %v", tt.period, tt.now.Format(time.RFC3339), got, tt.want)
		}
	}
}

// Compacting December and the year in one run must produce the yearly file
// directly, with every record in date order. Passes at deabcc6.
func TestAggregateDecemberAndYearInOneRun(t *testing.T) {
	dir := t.TempDir()
	records := dailyRecords("pkg", utcDate(2024, 1, 1), utcDate(2024, 12, 31))
	if _, err := WriteRecords(dir, "pypi", "proj", records); err != nil {
		t.Fatal(err)
	}
	if err := Aggregate(dir, time.Date(2025, 1, 1, 1, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if names := projectFileNames(t, MetricsProjectDir(dir, "pypi", "proj")); len(names) != 1 || names[0] != "2024.jsonl" {
		t.Fatalf("files = %v, want [2024.jsonl]", names)
	}
	got, err := ReadRecords(metricsPath(dir, "pypi", "proj", "2024"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(records) {
		t.Fatalf("got %d records, want %d", len(got), len(records))
	}
	for i := range got {
		if got[i].Date != records[i].Date || got[i].Value != records[i].Value {
			t.Fatalf("record %d = %s/%d, want %s/%d", i, got[i].Date, got[i].Value, records[i].Date, records[i].Value)
		}
	}
}

// Re-fetching a day (e.g. `fetch --start-date D --end-date D`) must not drop a
// total_* change point that was recorded on that day. Fails at deabcc6: the
// "unchanged" comparison uses the file being rewritten as its baseline, so
// total_likes is dropped from D's file and the series silently reverts to the
// value from the day before.
func TestWriteRecordsRefetchKeepsChangePointsOfThatDay(t *testing.T) {
	dir := t.TempDir()
	total := func(metric, date string, value int64) source.Record {
		return source.Record{Metric: metric, ProjectID: "proj", Target: "ext", Date: date, Value: value}
	}
	if _, err := WriteRecords(dir, "openvsx", "proj", []source.Record{
		total("total_downloads", "2025-06-01", 9), total("total_likes", "2025-06-01", 4),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteRecords(dir, "openvsx", "proj", []source.Record{
		total("total_downloads", "2025-06-02", 10), total("total_likes", "2025-06-02", 5),
	}); err != nil {
		t.Fatal(err)
	}
	// Re-fetch 2025-06-02: downloads moved on, likes did not.
	if _, err := WriteRecords(dir, "openvsx", "proj", []source.Record{
		total("total_downloads", "2025-06-02", 11), total("total_likes", "2025-06-02", 5),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := ReadRecords(metricsPath(dir, "openvsx", "proj", "2025-06-02"))
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]int64{}
	for _, r := range got {
		values[r.Metric] = r.Value
	}
	if values["total_downloads"] != 11 || values["total_likes"] != 5 {
		t.Errorf("2025-06-02 after re-fetch = %v, want total_downloads=11 and total_likes=5 (total_likes changed from 4 to 5 on this day)", values)
	}
}

// A crash (or cancelled CI job) part-way through deleting the daily files
// leaves the complete monthly file plus some of the daily files. The next run
// must not shrink the month to the leftovers. Fails at deabcc6: the monthly
// file is rebuilt from the 12 remaining daily files only.
func TestAggregateRecoversFromCrashDuringDelete(t *testing.T) {
	dir := t.TempDir()
	records := dailyRecords("pkg", utcDate(2025, 1, 1), utcDate(2025, 1, 31))
	for i := range records {
		records[i].Source = "pypi"
	}
	writeTestRecords(t, metricsPath(dir, "pypi", "proj", "2025-01"), records...)
	if _, err := WriteRecords(dir, "pypi", "proj", dailyRecords("pkg", utcDate(2025, 1, 20), utcDate(2025, 1, 31))); err != nil {
		t.Fatal(err)
	}

	if err := Aggregate(dir, utcDate(2025, 2, 2)); err != nil {
		t.Fatal(err)
	}

	if got := len(readProjectRecords(t, dir, "pypi", "proj")); got != 31 {
		t.Fatalf("after recovering from an interrupted delete: %d records, want 31", got)
	}
}
