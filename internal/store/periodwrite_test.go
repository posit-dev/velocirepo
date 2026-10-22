package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/source"
)

func rec(target, metric, date string, v int64) source.Record {
	return source.Record{Metric: metric, ProjectID: "proj", Target: target, Date: date, Value: v}
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func concatenatedOctober(t *testing.T, dataDir string) {
	t.Helper()
	var records []source.Record
	for d := 1; d <= 31; d++ {
		records = append(records, rec("pkg", "daily_downloads", time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), 1))
	}
	if _, err := WriteRecords(dataDir, "pypi", "proj", records); err != nil {
		t.Fatal(err)
	}
	if err := Aggregate(dataDir, time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if got := files(t, MetricsProjectDir(dataDir, "pypi", "proj")); len(got) != 1 || got[0] != "2026-10.jsonl" {
		t.Fatalf("setup: files = %v", got)
	}
}

// Records for a period that's already concatenated go into its period file,
// so velocirepo never creates daily files next to a monthly or yearly file.
func TestLateRecordsGoIntoTheirPeriodFile(t *testing.T) {
	dataDir := t.TempDir()
	concatenatedOctober(t, dataDir)

	if _, err := WriteRecords(dataDir, "pypi", "proj", []source.Record{
		rec("new-target", "daily_downloads", "2026-10-05", 7),
		rec("pkg", "daily_downloads", "2026-10-06", 9),
	}); err != nil {
		t.Fatal(err)
	}
	dir := MetricsProjectDir(dataDir, "pypi", "proj")
	if got := files(t, dir); len(got) != 1 || got[0] != "2026-10.jsonl" {
		t.Errorf("files = %v, want only 2026-10.jsonl", got)
	}
	records, _ := ReadRecords(filepath.Join(dir, "2026-10.jsonl"))
	values := map[string]int64{}
	for _, r := range records {
		values[r.Target+" "+r.Date] = r.Value
	}
	if len(records) != 32 || values["new-target 2026-10-05"] != 7 || values["pkg 2026-10-06"] != 9 {
		t.Errorf("got %d records, values %v", len(records), values)
	}
	result, err := ValidateData(dataDir, map[string]bool{"proj": true})
	if err != nil || len(result.Issues) != 0 {
		t.Errorf("validate-data = %+v, %v", result.Issues, err)
	}
}

// A scoped write replaces, for its targets and days, the records of its
// metrics: it can correct a day to "no data", and leaves other metrics alone.
func TestScopedRecordWrites(t *testing.T) {
	dataDir := t.TempDir()
	if _, err := WriteRecords(dataDir, "github", "proj", []source.Record{
		rec("org/a", "daily_stars", "2026-10-05", 3),
		rec("org/a", "daily_stars", "2026-10-06", 1),
		rec("org/a", "total_reactions", "2026-10-06", 4),
		rec("org/b", "daily_stars", "2026-10-06", 2),
	}); err != nil {
		t.Fatal(err)
	}
	// The repair found no stars on the 6th for org/a, and 5 on the 5th.
	if _, err := WriteRecordsScoped(dataDir, "github", "proj",
		[]source.Record{rec("org/a", "daily_stars", "2026-10-05", 5)},
		RecordScope{Windows: map[string]source.Window{"org/a": {Start: day(5), End: day(6)}}, Metrics: []string{"daily_stars"}},
	); err != nil {
		t.Fatal(err)
	}

	got := map[string]int64{}
	for _, date := range []string{"2026-10-05", "2026-10-06"} {
		records, _ := ReadRecords(filepath.Join(MetricsProjectDir(dataDir, "github", "proj"), date+".jsonl"))
		for _, r := range records {
			got[r.Target+" "+r.Metric+" "+r.Date] = r.Value
		}
	}
	want := map[string]int64{
		"org/a daily_stars 2026-10-05":     5,
		"org/a total_reactions 2026-10-06": 4,
		"org/b daily_stars 2026-10-06":     2,
	}
	if len(got) != len(want) {
		t.Errorf("records = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
}

// Scoped event writes also replace events in a period file: a repaired event
// with a changed Extra replaces the old one instead of sitting next to it.
func TestScopedEventWritesIntoAPeriodFile(t *testing.T) {
	dataDir := t.TempDir()
	old := source.Event{Type: "comment", ProjectID: "proj", Target: "org/a", Datetime: "2026-10-05T10:00:00Z", User: "u", Extra: map[string]string{"x": "old"}}
	if _, err := WriteEvents(dataDir, "github", "proj", []source.Event{old}); err != nil {
		t.Fatal(err)
	}
	if err := MarkFetchedSeries(dataDir, "github", "proj", "", "2026-10-31", []string{"org/a"}); err != nil {
		t.Fatal(err)
	}
	if err := Aggregate(dataDir, time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	fixed := old
	fixed.Extra = map[string]string{"x": "new"}
	if _, err := WriteEventsScoped(dataDir, "github", "proj", []source.Event{fixed}, map[string]source.Window{"org/a": {Start: day(5), End: day(5)}}); err != nil {
		t.Fatal(err)
	}
	dir := EventsProjectDir(dataDir, "github", "proj")
	if got := files(t, dir); len(got) != 1 || got[0] != "2026-10.jsonl" {
		t.Fatalf("files = %v, want only 2026-10.jsonl", got)
	}
	events, _ := ReadEvents(filepath.Join(dir, "2026-10.jsonl"))
	if len(events) != 1 || events[0].Extra["x"] != "new" {
		t.Errorf("events = %+v, want only the repaired event", events)
	}
}

// Concatenation waits for the concatenation day of the following month, and
// for the project's data to be complete. A missed day is caught up the next
// run, and December comes with the year.
func TestConcatenationPolicy(t *testing.T) {
	dataDir := t.TempDir()
	var records []source.Record
	for d := 1; d <= 31; d++ {
		records = append(records, rec("pkg", "daily_downloads", time.Date(2025, 12, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), 1))
	}
	if _, err := WriteRecords(dataDir, "pypi", "proj", append(records, rec("pkg", "daily_downloads", "2025-11-30", 1))); err != nil {
		t.Fatal(err)
	}
	ready := true
	policy := Policy{Day: 8, Ready: func(category, sourceName, projectID, lastDay string) bool { return ready }}
	dir := MetricsProjectDir(dataDir, "pypi", "proj")

	if err := AggregateWithPolicy(dataDir, time.Date(2026, 1, 7, 9, 30, 0, 0, time.UTC), policy); err != nil {
		t.Fatal(err)
	}
	for _, f := range files(t, dir) {
		if f == "2025-12.jsonl" || f == "2025.jsonl" {
			t.Errorf("concatenated before the 8th: %v", files(t, dir))
		}
	}

	ready = false
	if err := AggregateWithPolicy(dataDir, time.Date(2026, 1, 8, 9, 30, 0, 0, time.UTC), policy); err != nil {
		t.Fatal(err)
	}
	if got := files(t, dir); len(got) < 30 {
		t.Errorf("concatenated a project that isn't ready: %v", got)
	}

	ready = true
	if err := AggregateWithPolicy(dataDir, time.Date(2026, 1, 9, 9, 30, 0, 0, time.UTC), policy); err != nil {
		t.Fatal(err)
	}
	if got := files(t, dir); len(got) != 1 || got[0] != "2025.jsonl" {
		t.Errorf("files on the 9th = %v, want December and the year concatenated into 2025.jsonl", got)
	}
}
