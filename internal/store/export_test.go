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
	if _, err := WriteRecords(dataDir, "pypi", "my-proj", records); err != nil {
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

	if len(written) != len(ExportTables) {
		t.Fatalf("expected %d files, got %d", len(ExportTables), len(written))
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
	if _, err := WriteRecords(dataDir, "pypi", "pkg", records); err != nil {
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

	if len(written) != len(ExportTables) {
		t.Fatalf("expected %d files, got %d", len(ExportTables), len(written))
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
	if _, err := WriteRecords(dataDir, "pypi", "test", records); err != nil {
		t.Fatal(err)
	}

	events := []source.Event{
		{Type: "star", ProjectID: "test", Target: "owner/repo", Datetime: "2025-06-01T10:00:00Z", User: "alice"},
	}
	if _, err := WriteEvents(dataDir, "github", "test", events); err != nil {
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

	if len(written) != len(ExportTables) {
		t.Fatalf("expected %d files with source filter, got %d", len(ExportTables), len(written))
	}
	count := func(table string) int64 {
		results, _, err := QueryLive(dataDir, nil, nil, "SELECT COUNT(*) AS cnt FROM '"+filepath.Join(outDir, table+".parquet")+"'")
		if err != nil {
			t.Fatal(err)
		}
		return results[0]["cnt"].(int64)
	}
	if n := count("events"); n != 1 {
		t.Errorf("events has %d rows, want the github event", n)
	}
	if n := count("metrics"); n != 0 {
		t.Errorf("metrics has %d rows, want none: the pypi record isn't github's", n)
	}
}

func TestExportWithProjectFilter(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	records := []source.Record{
		{Metric: "stars", ProjectID: "proj-a", Date: "2025-06-01", Value: 10},
		{Metric: "stars", ProjectID: "proj-b", Date: "2025-06-01", Value: 20},
	}
	if _, err := WriteRecords(dataDir, "pypi", "proj-a", records[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteRecords(dataDir, "pypi", "proj-b", records[1:]); err != nil {
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

func TestExportContentProjectFilter(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	entriesA := []source.ContentEntry{
		{Source: "youtube", Target: "@ChannelA", ID: "vid-a1", Title: "Video A1", ProjectID: "proj-a"},
		{Source: "youtube", Target: "@ChannelA", ID: "vid-a2", Title: "Video A2", ProjectID: "proj-a"},
	}
	entriesB := []source.ContentEntry{
		{Source: "youtube", Target: "@ChannelB", ID: "vid-b1", Title: "Video B1", ProjectID: "proj-b"},
	}
	if err := WriteContent(dataDir, "youtube", "proj-a", "videos.jsonl", entriesA); err != nil {
		t.Fatal(err)
	}
	if err := WriteContent(dataDir, "youtube", "proj-b", "videos.jsonl", entriesB); err != nil {
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

	results, _, err := QueryLive(dataDir, nil, nil,
		"SELECT COUNT(*) AS cnt FROM '"+filepath.Join(outDir, "content.parquet")+"'")
	if err != nil {
		t.Fatal(err)
	}
	cnt := results[0]["cnt"].(int64)
	if cnt != 2 {
		t.Fatalf("export --project proj-a: expected 2 content rows, got %d", cnt)
	}

	// proj-b's video must be absent from the export.
	results, _, err = QueryLive(dataDir, nil, nil,
		"SELECT COUNT(*) AS cnt FROM '"+filepath.Join(outDir, "content.parquet")+"' WHERE project = 'proj-b'")
	if err != nil {
		t.Fatal(err)
	}
	if results[0]["cnt"].(int64) != 0 {
		t.Fatalf("export --project proj-a: proj-b content leaked into export")
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

	if len(written) != len(ExportTables) {
		t.Fatalf("expected %d files even for empty data, got %d", len(ExportTables), len(written))
	}
}
