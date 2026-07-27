package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/posit-dev/velocirepo/internal/source"
)

func TestExportParquet(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	records := []source.Record{
		{Metric: "stars", ProjectID: "my-proj", Date: "2025-06-01", Value: 10},
		{Metric: "forks", ProjectID: "my-proj", Date: "2025-06-01", Value: 3},
	}
	if err := WriteRecords(dataDir, "pypi", "my-proj", records); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(dir, "out")
	written, err := Export(ExportOptions{
		DataDir: dataDir,
		OutDir:  outDir,
		Format:  "parquet",
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(written) != 5 {
		t.Fatalf("expected 5 files, got %d", len(written))
	}

	metricsPath := filepath.Join(outDir, "metrics.parquet")
	info, err := os.Stat(metricsPath)
	if err != nil {
		t.Fatal("metrics.parquet not created")
	}
	if info.Size() == 0 {
		t.Fatal("metrics.parquet is empty")
	}
}

func TestExportCSV(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	records := []source.Record{
		{Metric: "downloads", ProjectID: "pkg", Date: "2025-06-01", Value: 500, Extra: map[string]string{"version": "1.0"}},
		{Metric: "downloads", ProjectID: "pkg", Date: "2025-06-02", Value: 600},
	}
	if err := WriteRecords(dataDir, "pypi", "pkg", records); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(dir, "out")
	written, err := Export(ExportOptions{
		DataDir: dataDir,
		OutDir:  outDir,
		Format:  "csv",
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(written) != 5 {
		t.Fatalf("expected 5 files, got %d", len(written))
	}

	data, err := os.ReadFile(filepath.Join(outDir, "metrics.csv"))
	if err != nil {
		t.Fatal("metrics.csv not created")
	}
	if len(data) == 0 {
		t.Fatal("metrics.csv is empty")
	}
}

func TestExportWithSourceFilter(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	records := []source.Record{
		{Metric: "stars", ProjectID: "test", Date: "2025-06-01", Value: 10},
	}
	if err := WriteRecords(dataDir, "pypi", "test", records); err != nil {
		t.Fatal(err)
	}

	events := []source.Event{
		{Type: "star", ProjectID: "test", Target: "owner/repo", Datetime: "2025-06-01T10:00:00Z", User: "alice"},
	}
	if err := WriteEvents(dataDir, "github", "test", events); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(dir, "out")
	written, err := Export(ExportOptions{
		DataDir: dataDir,
		OutDir:  outDir,
		Format:  "parquet",
		Source:  "github",
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(written) != 1 {
		t.Fatalf("expected 1 file with source filter, got %d", len(written))
	}
	if filepath.Base(written[0]) != "events.parquet" {
		t.Errorf("expected events.parquet, got %s", filepath.Base(written[0]))
	}
}

func TestExportWithProjectFilter(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	records := []source.Record{
		{Metric: "stars", ProjectID: "proj-a", Date: "2025-06-01", Value: 10},
		{Metric: "stars", ProjectID: "proj-b", Date: "2025-06-01", Value: 20},
	}
	if err := WriteRecords(dataDir, "pypi", "proj-a", records[:1]); err != nil {
		t.Fatal(err)
	}
	if err := WriteRecords(dataDir, "pypi", "proj-b", records[1:]); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(dir, "out")
	_, err := Export(ExportOptions{
		DataDir: dataDir,
		OutDir:  outDir,
		Format:  "parquet",
		Project: "proj-a",
	})
	if err != nil {
		t.Fatal(err)
	}

	results, _, err := QueryLive(dataDir, nil, nil, "SELECT COUNT(*) AS cnt FROM '"+filepath.Join(outDir, "metrics.parquet")+"'")
	if err != nil {
		t.Fatal(err)
	}
	cnt := results[0]["cnt"].(int64)
	if cnt != 1 {
		t.Fatalf("expected 1 row (filtered to proj-a), got %d", cnt)
	}
}

func TestExportEmptyData(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	_ = os.MkdirAll(dataDir, 0755)

	outDir := filepath.Join(dir, "out")
	written, err := Export(ExportOptions{
		DataDir: dataDir,
		OutDir:  outDir,
		Format:  "parquet",
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(written) != 5 {
		t.Fatalf("expected 5 files even for empty data, got %d", len(written))
	}
}
