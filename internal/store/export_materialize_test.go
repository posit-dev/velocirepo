package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/posit-dev/velocirepo/internal/source"
)

func intPtr(n int) *int { return &n }

// writeExportFixture writes a little of every kind of data: per-page and
// per-video metrics, GitHub events and issues.
func writeExportFixture(t *testing.T, dataDir string) []ProjectInfo {
	t.Helper()
	if _, err := WriteRecords(dataDir, "plausible", "site", []source.Record{
		{Metric: "daily_site_pageviews", ProjectID: "site", Target: "example.com", Date: "2026-01-01", Value: 10},
		{Metric: "daily_pageviews", ProjectID: "site", Target: "example.com", Date: "2026-01-01", Value: 7, Extra: map[string]string{"page": "/"}},
		{Metric: "daily_pageviews", ProjectID: "site", Target: "example.com", Date: "2026-01-01", Value: 3, Extra: map[string]string{"page": "/docs"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteRecords(dataDir, "github", "pkg", []source.Record{
		{Metric: "total_reactions", ProjectID: "pkg", Target: "o/pkg", Date: "2026-01-02", Value: 4, Extra: map[string]string{"ref": "12"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteEvents(dataDir, "github", "pkg", []source.Event{
		{Type: "issue_open", ProjectID: "pkg", Target: "o/pkg", Datetime: "2026-01-01T10:00:00Z", Ref: intPtr(12), User: "alice"},
		{Type: "comment", ProjectID: "pkg", Target: "o/pkg", Datetime: "2026-01-02T11:00:00Z", Ref: intPtr(12), User: "bob"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := WriteContent(dataDir, "github", "pkg", "issues.jsonl", []source.ContentEntry{
		{Source: "github", Target: "o/pkg", ID: "issue/12", Ref: intPtr(12), Type: "issue", Title: "Bug", PublishedAt: "2026-01-01T10:00:00Z",
			Tags: []string{"bug"}, Extra: map[string]any{"author": "alice", "state": "OPEN"}},
	}); err != nil {
		t.Fatal(err)
	}
	return []ProjectInfo{
		{ID: "pkg", Name: "Pkg", Tags: []string{"r"}, Targets: []ProjectTarget{{Source: "github", Target: "o/pkg", Canonical: true}}},
		{ID: "pkg-old", Name: "pkg", Targets: []ProjectTarget{{Source: "github", Target: "o/pkg", Canonical: false}}},
		{ID: "site", Name: "Site", Targets: []ProjectTarget{{Source: "plausible", Target: "example.com", Canonical: true}}},
	}
}

func TestExportDuckDBIsStandalone(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	projects := writeExportFixture(t, dataDir)

	outDir := filepath.Join(dir, "out")
	written, err := Export(ExportOptions{DataDir: dataDir, OutDir: outDir, Format: "duckdb", Projects: projects, Indicators: DefaultIndicators})
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 || filepath.Base(written[0]) != ExportDBName {
		t.Fatalf("written = %v", written)
	}

	db, err := sql.Open("duckdb", written[0]+"?access_mode=read_only")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var views, tables, jsonColumns int
	if err := db.QueryRow("SELECT COUNT(*) FROM duckdb_views() WHERE NOT internal").Scan(&views); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM duckdb_tables()").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM duckdb_columns() WHERE data_type = 'JSON'").Scan(&jsonColumns); err != nil {
		t.Fatal(err)
	}
	if views != 0 || tables != len(ExportTables) || jsonColumns != 0 {
		t.Errorf("views = %d, tables = %d, JSON columns = %d; want 0, %d, 0", views, tables, len(ExportTables), jsonColumns)
	}

	checks := map[string]string{
		"per-page row":       "SELECT COUNT(*) FROM metrics WHERE page = '/docs' AND value = 3",
		"site row":           "SELECT COUNT(*) FROM metrics WHERE metric = 'daily_site_pageviews' AND page IS NULL",
		"reaction ref":       "SELECT COUNT(*) FROM metrics WHERE metric = 'total_reactions' AND ref = 12",
		"derived from event": "SELECT COUNT(*) FROM metrics WHERE metric = 'daily_comments' AND value = 1",
		"filled":             "SELECT COUNT(*) FROM metrics_filled WHERE metric = 'total_reactions' AND ref = 12",
		"events":             `SELECT COUNT(*) FROM events WHERE "user" = 'bob' AND ref = 12`,
		"content columns":    "SELECT COUNT(*) FROM content WHERE author = 'alice' AND state = 'OPEN' AND list_contains(tags, 'bug')",
		"projects tags":      `SELECT COUNT(*) FROM projects WHERE id = 'pkg' AND list_contains(tags, 'r')`,
		"canonical":          "SELECT COUNT(*) FROM targets WHERE project = 'pkg' AND canonical",
		"duplicate":          "SELECT COUNT(*) FROM targets WHERE project = 'pkg-old' AND NOT canonical",
		"meta":               "SELECT COUNT(*) FROM meta WHERE as_of = DATE '2026-01-02' AND built_at IS NOT NULL",
	}
	if _, err := db.Exec("SELECT page, extra FROM indicators"); err != nil {
		t.Errorf("indicators lack the metrics columns: %v", err)
	}
	for name, query := range checks {
		var n int
		if err := db.QueryRow(query).Scan(&n); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if n < 1 {
			t.Errorf("%s: no rows for %s", name, query)
		}
	}
}

// Only the declared keys the data has become columns, so the export has no
// column that's always empty.
func TestExportColumnsFollowTheData(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	projects := writeExportFixture(t, dataDir)

	x, err := Materialize(ExportOptions{DataDir: dataDir, Format: "parquet", Projects: projects, Source: "plausible"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = x.Close() }()

	var keys []string
	for _, c := range x.Columns["metrics"] {
		keys = append(keys, c.Key)
	}
	if len(keys) != 1 || keys[0] != "page" {
		t.Errorf("metrics columns = %v, want [page]", keys)
	}
	if len(x.Columns["content"]) != 0 {
		t.Errorf("content columns = %v, want none", x.Columns["content"])
	}

	var n int
	if err := x.DB.QueryRow("SELECT COUNT(*) FROM export.projects").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("projects with a plausible target = %d, want 1", n)
	}
}

func TestExportEmptyDataHasEveryTable(t *testing.T) {
	dir := t.TempDir()
	written, err := Export(ExportOptions{DataDir: filepath.Join(dir, "data"), OutDir: filepath.Join(dir, "out"), Format: "duckdb"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("duckdb", written[0]+"?access_mode=read_only")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, table := range ExportTables {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Errorf("%s: %v", table, err)
		}
	}
}

// The daily metrics the metrics view counts from events are declared by the
// source of the events.
func TestMetricsFromEventsAreDeclared(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	meta, _ := source.MetaFor("github")
	declared := map[string]bool{}
	for _, m := range meta.Metrics {
		declared[m.Name] = true
	}
	var events []source.Event
	for _, e := range meta.Events {
		events = append(events, source.Event{Type: e.Name, ProjectID: "p", Target: "o/p", Datetime: "2026-01-01T10:00:00Z"})
	}
	if _, err := WriteEvents(dataDir, "github", "p", events); err != nil {
		t.Fatal(err)
	}
	rows, _, err := QueryLive(dataDir, nil, nil, "SELECT DISTINCT metric FROM metrics")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(meta.Events) {
		t.Errorf("%d event types give %d metrics", len(meta.Events), len(rows))
	}
	for _, r := range rows {
		if m := r["metric"].(string); !declared[m] {
			t.Errorf("events give metric %s, which github doesn't declare", m)
		}
	}
}
