package store

import (
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

	if err := WriteRecords(dir, "pypi", "mylib", records); err != nil {
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

	if err := WriteRecords(dir, "youtube", "mylib", records); err != nil {
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
