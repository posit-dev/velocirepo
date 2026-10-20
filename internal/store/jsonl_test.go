package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/source"
)

func TestWriteAndReadRecords(t *testing.T) {
	dir := t.TempDir()
	records := []source.Record{
		{Metric: "downloads", ProjectID: "mylib", Date: "2025-06-01", Value: 100},
		{Metric: "downloads", ProjectID: "mylib", Date: "2025-06-01", Value: 200},
		{Metric: "downloads", ProjectID: "mylib", Date: "2025-06-02", Value: 150},
	}

	if _, err := WriteRecords(dir, "pypi", "mylib", records); err != nil {
		t.Fatalf("WriteRecords failed: %v", err)
	}

	// Check files exist
	path1 := metricsPath(dir, "pypi", "mylib", "2025-06-01")
	path2 := metricsPath(dir, "pypi", "mylib", "2025-06-02")

	// Read back day 1
	got, err := ReadRecords(path1)
	if err != nil {
		t.Fatalf("ReadRecords failed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2", len(got))
	}
	if got[0].Value != 100 {
		t.Errorf("got[0].Value = %d, want 100", got[0].Value)
	}
	if got[1].Value != 200 {
		t.Errorf("got[1].Value = %d, want 200", got[1].Value)
	}

	// Read back day 2
	got, err = ReadRecords(path2)
	if err != nil {
		t.Fatalf("ReadRecords failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
}

func TestWriteRecordsWithTags(t *testing.T) {
	dir := t.TempDir()
	records := []source.Record{
		{Metric: "views", ProjectID: "mylib", Date: "2025-06-01", Value: 500, Extra: map[string]string{"video_id": "abc123"}},
	}

	if _, err := WriteRecords(dir, "youtube", "mylib", records); err != nil {
		t.Fatalf("WriteRecords failed: %v", err)
	}

	path := metricsPath(dir, "youtube", "mylib", "2025-06-01")
	got, err := ReadRecords(path)
	if err != nil {
		t.Fatalf("ReadRecords failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0].Extra["video_id"] != "abc123" {
		t.Errorf("Tags[video_id] = %q, want %q", got[0].Extra["video_id"], "abc123")
	}
}

func TestLastDateDaily(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"2025-01-15.jsonl", "2025-03-20.jsonl", "2025-02-10.jsonl"} {
		writeTestRaw(t, eventsPath(dir, "github", "myproject", name), []string{`{}`})
	}

	got, err := LastDate(dir, "github", "myproject")
	if err != nil {
		t.Fatalf("LastDate failed: %v", err)
	}

	want := time.Date(2025, 3, 20, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("LastDate = %v, want %v", got, want)
	}
}

func TestLastDateMonthly(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"2025-01.jsonl", "2025-03.jsonl"} {
		writeTestRaw(t, metricsPath(dir, "pypi", "mylib", name), []string{`{}`})
	}

	got, err := LastDate(dir, "pypi", "mylib")
	if err != nil {
		t.Fatalf("LastDate failed: %v", err)
	}

	want := time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("LastDate = %v, want %v", got, want)
	}
}

func TestLastDateYearly(t *testing.T) {
	dir := t.TempDir()

	writeTestRaw(t, metricsPath(dir, "cran", "pkg", "2024"), []string{`{}`})

	got, err := LastDate(dir, "cran", "pkg")
	if err != nil {
		t.Fatalf("LastDate failed: %v", err)
	}

	want := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("LastDate = %v, want %v", got, want)
	}
}

func TestLastDateEmpty(t *testing.T) {
	dir := t.TempDir()

	got, err := LastDate(dir, "github", "nonexistent")
	if err != nil {
		t.Fatalf("LastDate failed: %v", err)
	}

	if !got.IsZero() {
		t.Errorf("LastDate = %v, want zero time", got)
	}
}

func TestLastDateMixed(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"2024.jsonl", "2025-01.jsonl", "2025-02-15.jsonl"} {
		writeTestRaw(t, eventsPath(dir, "github", "proj", name), []string{`{}`})
	}

	got, err := LastDate(dir, "github", "proj")
	if err != nil {
		t.Fatalf("LastDate failed: %v", err)
	}

	want := time.Date(2025, 2, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("LastDate = %v, want %v", got, want)
	}
}

// Writing records for a day that already has a daily file merges them into it:
// records of other series stay, and the new record of a series replaces the
// stored one. The star history writes past days that other metrics of the
// same source were stored on.
func TestWriteRecordsMergesIntoExistingDailyFile(t *testing.T) {
	dataDir := t.TempDir()
	first := []source.Record{
		{Metric: "total_reactions", ProjectID: "proj", Target: "org/repo", Date: "2025-06-10", Value: 5, Extra: map[string]string{"ref": "1"}},
		{Metric: "daily_stars", ProjectID: "proj", Target: "org/repo", Date: "2025-06-10", Value: 2},
	}
	if _, err := WriteRecords(dataDir, "github", "proj", first); err != nil {
		t.Fatal(err)
	}
	second := []source.Record{
		{Metric: "daily_stars", ProjectID: "proj", Target: "org/repo", Date: "2025-06-10", Value: 1},
	}
	if _, err := WriteRecords(dataDir, "github", "proj", second); err != nil {
		t.Fatal(err)
	}

	got, err := ReadRecords(filepath.Join(MetricsProjectDir(dataDir, "github", "proj"), "2025-06-10.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]int64)
	for _, r := range got {
		values[r.Metric] = r.Value
	}
	if len(got) != 2 || values["total_reactions"] != 5 || values["daily_stars"] != 1 {
		t.Errorf("records = %+v, want total_reactions 5 kept and daily_stars replaced by 1", got)
	}
}

// A total fetched again on the same day replaces the value stored for that
// day, even when it's back at the value of the day before and so isn't stored
// itself.
func TestWriteRecordsReplacesTotalThatReturnsToItsPreviousValue(t *testing.T) {
	dataDir := t.TempDir()
	write := func(date string, value int64) {
		t.Helper()
		r := []source.Record{{Metric: "total_stars", ProjectID: "proj", Target: "org/repo", Date: date, Value: value}}
		if _, err := WriteRecords(dataDir, "github", "proj", r); err != nil {
			t.Fatal(err)
		}
	}
	write("2025-06-09", 5)
	write("2025-06-10", 6)
	write("2025-06-10", 5)

	got, err := ReadRecords(filepath.Join(MetricsProjectDir(dataDir, "github", "proj"), "2025-06-10.jsonl"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	for _, r := range got {
		if r.Value == 6 {
			t.Errorf("2025-06-10 still holds the replaced total: %+v", got)
		}
	}
}
