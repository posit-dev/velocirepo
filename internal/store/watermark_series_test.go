package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSeriesWatermarksAreKeptApart(t *testing.T) {
	dataDir := t.TempDir()
	if err := MarkFetchedSeries(dataDir, "github", "proj", "", "2026-10-20", []string{"org/a"}); err != nil {
		t.Fatal(err)
	}
	if err := MarkFetchedSeries(dataDir, "github", "proj", "stars", "2026-10-19", []string{"org/a"}); err != nil {
		t.Fatal(err)
	}

	main, err := SeriesWatermarks(dataDir, "github", "proj", "")
	if err != nil || main["org/a"] != "2026-10-20" {
		t.Errorf("main series = %v, %v; want org/a at 2026-10-20", main, err)
	}
	stars, err := SeriesWatermarks(dataDir, "github", "proj", "stars")
	if err != nil || stars["org/a"] != "2026-10-19" {
		t.Errorf("stars series = %v, %v; want org/a at 2026-10-19", stars, err)
	}

	// The main series stays in _watermark.json, so older binaries keep
	// reading it; other series get their own file, which they ignore.
	if _, err := os.Stat(filepath.Join(MetricsProjectDir(dataDir, "github", "proj"), "_watermark-stars.json")); err != nil {
		t.Errorf("stars watermark file: %v", err)
	}
}

func TestIsWatermarkFile(t *testing.T) {
	for name, want := range map[string]bool{
		"_watermark.json":       true,
		"_watermark-stars.json": true,
		"2026-10-20.jsonl":      false,
		"_watermark-.json":      false,
	} {
		if got := IsWatermarkFile(name); got != want {
			t.Errorf("IsWatermarkFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestValidateDataSkipsSeriesWatermarks(t *testing.T) {
	dataDir := t.TempDir()
	if err := MarkFetchedSeries(dataDir, "github", "proj", "stars", "2026-10-19", []string{"org/a"}); err != nil {
		t.Fatal(err)
	}
	result, err := ValidateData(dataDir, map[string]bool{"proj": true})
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range result.Issues {
		t.Errorf("unexpected issue: %+v", issue)
	}
}
