package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/source"
)

func TestFilterUnchangedTotals_NoPrevious(t *testing.T) {
	records := []source.Record{
		{Metric: "total_downloads", Value: 100},
		{Metric: "daily_downloads", Value: 5},
	}
	got := filterUnchangedTotals(records, nil)
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2", len(got))
	}
}

func TestFilterUnchangedTotals_SameValue(t *testing.T) {
	last := map[string]int64{"total_downloads|ext/foo": 100}
	records := []source.Record{
		{Metric: "total_downloads", Target: "ext/foo", Value: 100},
		{Metric: "daily_views", Target: "ext/foo", Value: 5},
	}
	got := filterUnchangedTotals(records, last)
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0].Metric != "daily_views" {
		t.Errorf("expected daily_views, got %s", got[0].Metric)
	}
}

func TestFilterUnchangedTotals_ChangedValue(t *testing.T) {
	last := map[string]int64{"total_downloads|ext/foo": 100}
	records := []source.Record{
		{Metric: "total_downloads", Target: "ext/foo", Value: 101},
	}
	got := filterUnchangedTotals(records, last)
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
}

func TestFilterUnchangedTotals_WithTags(t *testing.T) {
	last := map[string]int64{"total_views|@chan|video_id=abc": 500}
	records := []source.Record{
		{Metric: "total_views", Target: "@chan", Value: 500, Extra: map[string]string{"video_id": "abc"}},
		{Metric: "total_views", Target: "@chan", Value: 200, Extra: map[string]string{"video_id": "xyz"}},
	}
	got := filterUnchangedTotals(records, last)
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0].Extra["video_id"] != "xyz" {
		t.Errorf("expected video xyz, got %s", got[0].Extra["video_id"])
	}
}

func TestLastRecordedTotals_ReadsLatestFile(t *testing.T) {
	dir := t.TempDir()
	metricsDir := filepath.Join(dir, "metrics", "openvsx", "proj")
	if err := os.MkdirAll(metricsDir, 0755); err != nil {
		t.Fatal(err)
	}

	writeTestRecords(t, filepath.Join(metricsDir, "2025-06-01.jsonl"),
		source.Record{Metric: "total_downloads", Target: "ns/ext", Value: 100, Date: "2025-06-01"},
	)
	writeTestRecords(t, filepath.Join(metricsDir, "2025-06-05.jsonl"),
		source.Record{Metric: "total_downloads", Target: "ns/ext", Value: 150, Date: "2025-06-05"},
	)

	got, err := lastRecordedTotals(metricsDir)
	if err != nil {
		t.Fatal(err)
	}
	if got["total_downloads|ns/ext"] != 150 {
		t.Errorf("got %d, want 150", got["total_downloads|ns/ext"])
	}
}

func TestLastRecordedTotals_ReadsSparseKeysAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	metricsDir := filepath.Join(dir, "metrics", "openvsx", "proj")
	if err := os.MkdirAll(metricsDir, 0755); err != nil {
		t.Fatal(err)
	}

	writeTestRecords(t, filepath.Join(metricsDir, "2025-06-01.jsonl"),
		source.Record{Metric: "total_downloads", Target: "ns/ext", Value: 100, Date: "2025-06-01"},
		source.Record{Metric: "total_reviews", Target: "ns/ext", Value: 5, Date: "2025-06-01"},
	)
	writeTestRecords(t, filepath.Join(metricsDir, "2025-06-05.jsonl"),
		source.Record{Metric: "total_downloads", Target: "ns/ext", Value: 150, Date: "2025-06-05"},
	)

	got, err := lastRecordedTotals(metricsDir)
	if err != nil {
		t.Fatal(err)
	}
	if got["total_downloads|ns/ext"] != 150 {
		t.Errorf("downloads = %d, want 150", got["total_downloads|ns/ext"])
	}
	if got["total_reviews|ns/ext"] != 5 {
		t.Errorf("reviews = %d, want 5", got["total_reviews|ns/ext"])
	}
}

func TestLastRecordedTotals_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	got, err := lastRecordedTotals(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestLastRecordedTotals_NonexistentDir(t *testing.T) {
	got, err := lastRecordedTotals("/nonexistent/path")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestWriteRecords_SuppressesUnchangedTotals(t *testing.T) {
	dir := t.TempDir()

	// First write: establishes baseline
	records1 := []source.Record{
		{Metric: "total_downloads", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-01", Value: 100},
		{Metric: "total_reviews", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-01", Value: 5},
	}
	if _, err := WriteRecords(dir, "openvsx", "proj", records1); err != nil {
		t.Fatal(err)
	}

	// Second write: same values — should not create file
	records2 := []source.Record{
		{Metric: "total_downloads", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-02", Value: 100},
		{Metric: "total_reviews", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-02", Value: 5},
	}
	if _, err := WriteRecords(dir, "openvsx", "proj", records2); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "metrics", "openvsx", "proj", "2025-06-02.jsonl")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected 2025-06-02.jsonl to not exist, but it does")
	}

	got, err := LastDate(dir, "openvsx", "proj")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2025, 6, 2, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("LastDate = %v, want %v", got, want)
	}
}

func TestWriteRecords_WritesChangedTotals(t *testing.T) {
	dir := t.TempDir()

	records1 := []source.Record{
		{Metric: "total_downloads", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-01", Value: 100},
	}
	if _, err := WriteRecords(dir, "openvsx", "proj", records1); err != nil {
		t.Fatal(err)
	}

	records2 := []source.Record{
		{Metric: "total_downloads", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-02", Value: 105},
	}
	if _, err := WriteRecords(dir, "openvsx", "proj", records2); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "metrics", "openvsx", "proj", "2025-06-02.jsonl")
	got, err := ReadRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Value != 105 {
		t.Errorf("expected 1 record with value 105, got %v", got)
	}
}

func TestWriteRecords_MultiDateBatch(t *testing.T) {
	dir := t.TempDir()

	records1 := []source.Record{
		{Metric: "total_downloads", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-01", Value: 100},
	}
	if _, err := WriteRecords(dir, "openvsx", "proj", records1); err != nil {
		t.Fatal(err)
	}

	// Multi-date batch: day 2 unchanged, day 3 changes
	records2 := []source.Record{
		{Metric: "total_downloads", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-02", Value: 100},
		{Metric: "total_downloads", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-03", Value: 110},
		{Metric: "total_downloads", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-04", Value: 110},
	}
	if _, err := WriteRecords(dir, "openvsx", "proj", records2); err != nil {
		t.Fatal(err)
	}

	// Day 2: suppressed (unchanged from day 1)
	if _, err := os.Stat(filepath.Join(dir, "metrics", "openvsx", "proj", "2025-06-02.jsonl")); !os.IsNotExist(err) {
		t.Error("day 2 should be suppressed")
	}

	// Day 3: written (changed)
	got, err := ReadRecords(filepath.Join(dir, "metrics", "openvsx", "proj", "2025-06-03.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Value != 110 {
		t.Errorf("day 3: expected value 110, got %v", got)
	}

	// Day 4: suppressed (unchanged from day 3)
	if _, err := os.Stat(filepath.Join(dir, "metrics", "openvsx", "proj", "2025-06-04.jsonl")); !os.IsNotExist(err) {
		t.Error("day 4 should be suppressed")
	}
}

func TestWriteRecords_SuppressesSparseTotalsAcrossFiles(t *testing.T) {
	dir := t.TempDir()

	records1 := []source.Record{
		{Metric: "total_downloads", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-01", Value: 100},
		{Metric: "total_reviews", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-01", Value: 5},
	}
	if _, err := WriteRecords(dir, "openvsx", "proj", records1); err != nil {
		t.Fatal(err)
	}

	records2 := []source.Record{
		{Metric: "total_downloads", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-02", Value: 150},
		{Metric: "total_reviews", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-02", Value: 5},
	}
	if _, err := WriteRecords(dir, "openvsx", "proj", records2); err != nil {
		t.Fatal(err)
	}

	got, err := ReadRecords(filepath.Join(dir, "metrics", "openvsx", "proj", "2025-06-02.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Metric != "total_downloads" {
		t.Fatalf("day 2 should only contain changed downloads, got %v", got)
	}

	records3 := []source.Record{
		{Metric: "total_downloads", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-03", Value: 150},
		{Metric: "total_reviews", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-03", Value: 5},
	}
	if _, err := WriteRecords(dir, "openvsx", "proj", records3); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "metrics", "openvsx", "proj", "2025-06-03.jsonl")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected unchanged sparse totals to be suppressed on day 3")
	}
}

func TestWriteRecords_MixedDailyAndTotal(t *testing.T) {
	dir := t.TempDir()

	records1 := []source.Record{
		{Metric: "total_views", ProjectID: "proj", Target: "@ch", Date: "2025-06-01", Value: 1000, Extra: map[string]string{"video_id": "v1"}},
	}
	if _, err := WriteRecords(dir, "youtube", "proj", records1); err != nil {
		t.Fatal(err)
	}

	// Day 2: total unchanged but there's also a daily metric
	records2 := []source.Record{
		{Metric: "total_views", ProjectID: "proj", Target: "@ch", Date: "2025-06-02", Value: 1000, Extra: map[string]string{"video_id": "v1"}},
		{Metric: "total_subscribers", ProjectID: "proj", Target: "@ch", Date: "2025-06-02", Value: 50},
	}
	if _, err := WriteRecords(dir, "youtube", "proj", records2); err != nil {
		t.Fatal(err)
	}

	// Day 2 file should exist because total_subscribers is new (no previous value)
	got, err := ReadRecords(filepath.Join(dir, "metrics", "youtube", "proj", "2025-06-02.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 record, got %d", len(got))
	}
	if got[0].Metric != "total_subscribers" {
		t.Errorf("expected total_subscribers, got %s", got[0].Metric)
	}
}
