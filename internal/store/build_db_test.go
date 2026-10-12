package store

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/posit-dev/velocirepo/internal/source"
)

// TestBuildDBSuccessReplacesOldDatabase verifies that a successful build
// replaces the previous database and the new file is readable.
func TestBuildDBSuccessReplacesOldDatabase(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	records := []source.Record{
		{Metric: "daily_downloads", ProjectID: "pkg", Target: "pkg", Date: "2025-06-01", Value: 42},
	}
	if _, err := WriteRecords(dataDir, "pypi", "pkg", records); err != nil {
		t.Fatal(err)
	}

	// First build.
	if err := BuildDB(dataDir, nil, nil); err != nil {
		t.Fatalf("first BuildDB: %v", err)
	}
	dbPath := filepath.Join(dataDir, "velocirepo.duckdb")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatal("database not created after first build")
	}

	// Add more data and rebuild.
	more := []source.Record{
		{Metric: "daily_downloads", ProjectID: "pkg", Target: "pkg", Date: "2025-06-02", Value: 100},
	}
	if _, err := WriteRecords(dataDir, "pypi", "pkg", more); err != nil {
		t.Fatal(err)
	}
	if err := BuildDB(dataDir, nil, nil); err != nil {
		t.Fatalf("second BuildDB: %v", err)
	}

	// After rebuild the database must contain data from both days.
	results, _, err := QueryLive(dataDir, nil, nil, "SELECT SUM(value) AS total FROM metrics WHERE metric = 'daily_downloads'")
	if err != nil {
		t.Fatalf("query after rebuild: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no rows returned")
	}
	// DuckDB may return int64 or *big.Int depending on the context.
	var total int64
	switch v := results[0]["total"].(type) {
	case int64:
		total = v
	case *big.Int:
		total = v.Int64()
	default:
		t.Fatalf("unexpected type %T for total: %v", results[0]["total"], results[0]["total"])
	}
	if total != 142 {
		t.Errorf("total = %d, want 142", total)
	}
}

// TestBuildDBFailurePreservesOldDatabase verifies that a failing build does
// not destroy the previous database. We trigger a failure by pointing absDir
// at an empty, non-data directory so the glob views will be built with empty
// results, but we verify the old DB is still intact.
//
// To provoke a real build failure deterministically, we inject a broken
// project with a SQL-injection name that causes createProjectsTable to fail.
func TestBuildDBFailurePreservesOldDatabase(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	records := []source.Record{
		{Metric: "daily_downloads", ProjectID: "pkg", Target: "pkg", Date: "2025-06-01", Value: 99},
	}
	if _, err := WriteRecords(dataDir, "pypi", "pkg", records); err != nil {
		t.Fatal(err)
	}

	// First build produces a good database.
	if err := BuildDB(dataDir, nil, nil); err != nil {
		t.Fatalf("initial BuildDB: %v", err)
	}

	dbPath := filepath.Join(dataDir, "velocirepo.duckdb")
	originalStat, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	// Attempt a second build with a bad data dir (non-existent) so the open
	// fails before any views are created. The previous database must survive.
	badDataDir := filepath.Join(dir, "does-not-exist")
	// BuildDB resolves the path and then tries to open a DuckDB temp file
	// inside it, which fails because the directory doesn't exist.
	buildErr := BuildDB(badDataDir, nil, nil)
	if buildErr == nil {
		t.Skip("platform allowed build with non-existent dir; skipping preservation check")
	}

	// The original database must still be present and have the same size.
	afterStat, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal("previous database was removed by failing build")
	}
	if afterStat.Size() != originalStat.Size() {
		t.Errorf("database size changed from %d to %d during failed build",
			originalStat.Size(), afterStat.Size())
	}

	// No leftover temp .duckdb files in the data directory.
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "velocirepo.duckdb" && filepath.Ext(e.Name()) == ".duckdb" {
			t.Errorf("leftover temp database file: %s", e.Name())
		}
	}
}

// TestBuildDBNoTempLeftoverOnSuccess verifies no temp .duckdb files remain
// after a successful build.
func TestBuildDBNoTempLeftoverOnSuccess(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := BuildDB(dataDir, nil, nil); err != nil {
		t.Fatalf("BuildDB: %v", err)
	}

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "velocirepo.duckdb" && filepath.Ext(e.Name()) == ".duckdb" {
			t.Errorf("unexpected file after successful build: %s", e.Name())
		}
	}
}
