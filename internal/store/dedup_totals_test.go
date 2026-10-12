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

func TestLastRecordedTotalsBeforeDate(t *testing.T) {
	dir := t.TempDir()
	total := func(date string, value int64) source.Record {
		return source.Record{Source: "openvsx", Metric: "total_downloads", Target: "ext", Date: date, Value: value}
	}
	key := totalKey(total("", 0))
	keys := map[string]struct{}{key: {}}

	t.Run("ignores records on or after the cutoff", func(t *testing.T) {
		dir := t.TempDir()
		writeTestRecords(t, filepath.Join(dir, "2025-01-01.jsonl"), total("2025-01-01", 1))
		writeTestRecords(t, filepath.Join(dir, "2025-01-05.jsonl"), total("2025-01-05", 5))
		got, err := lastRecordedTotalsFor(dir, keys, "2025-01-05")
		if err != nil {
			t.Fatal(err)
		}
		if got[key] != 1 {
			t.Errorf("baseline before 2025-01-05 = %d, want 1", got[key])
		}
	})

	t.Run("a daily file wins over its period file for the same date", func(t *testing.T) {
		// The monthly file sorts first (it can hold later dates), so the
		// re-fetched daily value must win on the tie, not by order.
		writeTestRecords(t, filepath.Join(dir, "2025-01.jsonl"), total("2025-01-20", 5))
		writeTestRecords(t, filepath.Join(dir, "2025-01-20.jsonl"), total("2025-01-20", 7))
		got, err := lastRecordedTotalsFor(dir, keys, "2025-02-01")
		if err != nil {
			t.Fatal(err)
		}
		if got[key] != 7 {
			t.Errorf("baseline = %d, want 7 from the re-fetched daily file", got[key])
		}
	})

	t.Run("a period file spanning the cutoff only counts its earlier records", func(t *testing.T) {
		dir := t.TempDir()
		writeTestRecords(t, filepath.Join(dir, "2025-01.jsonl"), total("2025-01-10", 4), total("2025-01-15", 5), total("2025-01-20", 6))
		got, err := lastRecordedTotalsFor(dir, keys, "2025-01-15")
		if err != nil {
			t.Fatal(err)
		}
		if got[key] != 4 {
			t.Errorf("baseline before 2025-01-15 = %d, want 4 from 2025-01-10", got[key])
		}
	})

	t.Run("overlapping files don't end the search early", func(t *testing.T) {
		dir := t.TempDir()
		writeTestRecords(t, filepath.Join(dir, "2025-01.jsonl"), total("2025-01-10", 3))
		writeTestRecords(t, filepath.Join(dir, "2025-01-20.jsonl"), total("2025-01-20", 4))
		got, err := lastRecordedTotalsFor(dir, keys, "")
		if err != nil {
			t.Fatal(err)
		}
		if got[key] != 4 {
			t.Errorf("baseline = %d, want 4 from 2025-01-20", got[key])
		}
	})
}

// A total that goes down (an unstarred repo, a feed that drops items) is a
// change point too.
func TestWriteRecordsRecordsDecreasingTotals(t *testing.T) {
	dir := t.TempDir()
	for _, r := range []source.Record{
		{Metric: "total_items", ProjectID: "site", Target: "feed", Date: "2025-01-01", Value: 10},
		{Metric: "total_items", ProjectID: "site", Target: "feed", Date: "2025-01-02", Value: 8},
	} {
		if _, err := WriteRecords(dir, "rss", "site", []source.Record{r}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadRecords(metricsPath(dir, "rss", "site", "2025-01-02"))
	if err != nil {
		t.Fatalf("the decrease on 2025-01-02 was not stored: %v", err)
	}
	if len(got) != 1 || got[0].Value != 8 {
		t.Errorf("2025-01-02 = %+v, want total_items 8", got)
	}
}

// Backfilling an older day must not move the watermark backwards.
func TestWatermarkNeverMovesBackwards(t *testing.T) {
	dir := t.TempDir()
	for _, date := range []string{"2025-02-02", "2025-01-15"} {
		if _, err := WriteRecords(dir, "openvsx", "proj", []source.Record{
			{Metric: "total_downloads", ProjectID: "proj", Target: "ext", Date: date, Value: 10},
		}); err != nil {
			t.Fatal(err)
		}
	}
	watermarks, err := ReadMetricWatermarks(dir, "openvsx", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(watermarks) != 1 || watermarks[0].Date != "2025-02-02" {
		t.Errorf("watermarks = %+v, want ext at 2025-02-02", watermarks)
	}
}
