package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/source"
)

func TestQueryLive(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	records := []source.Record{
		{Metric: "stars", ProjectID: "my-proj", Date: "2025-06-01", Value: 10},
		{Metric: "forks", ProjectID: "my-proj", Date: "2025-06-01", Value: 3},
		{Metric: "stars", ProjectID: "my-proj", Date: "2025-06-02", Value: 15},
	}
	if err := WriteRecords(dataDir, "cran", "my-proj", records); err != nil {
		t.Fatal(err)
	}

	pypiRecords := []source.Record{
		{Metric: "downloads", ProjectID: "my-proj", Date: "2025-06-01", Value: 500, Tags: map[string]string{"version": "1.0.0"}},
	}
	if err := WriteRecords(dataDir, "pypi", "my-proj", pypiRecords); err != nil {
		t.Fatal(err)
	}

	results, _, err := QueryLive(dataDir, nil, nil, "SELECT COUNT(*) AS cnt FROM metrics")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 row, got %d", len(results))
	}
	cnt, ok := results[0]["cnt"].(int64)
	if !ok {
		t.Fatalf("unexpected count type: %T = %v", results[0]["cnt"], results[0]["cnt"])
	}
	if cnt != 4 {
		t.Fatalf("expected 4 rows, got %d", cnt)
	}

	results, _, err = QueryLive(dataDir, nil, nil, "SELECT DISTINCT source FROM metrics ORDER BY source")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(results))
	}
	if results[0]["source"] != "cran" {
		t.Errorf("expected first source=cran, got %v", results[0]["source"])
	}
	if results[1]["source"] != "pypi" {
		t.Errorf("expected second source=pypi, got %v", results[1]["source"])
	}

	results, _, err = QueryLive(dataDir, nil, nil, "SELECT metric, value FROM metrics WHERE source = 'pypi'")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 pypi row, got %d", len(results))
	}
	if results[0]["value"].(int64) != 500 {
		t.Errorf("expected value=500, got %v", results[0]["value"])
	}
}

func TestQueryLiveRestricted(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	records := []source.Record{
		{Metric: "downloads", ProjectID: "my-proj", Date: "2025-06-01", Value: 500},
	}
	if err := WriteRecords(dataDir, "pypi", "my-proj", records); err != nil {
		t.Fatal(err)
	}

	results, _, err := QueryLiveRestricted(dataDir, nil, nil, "SELECT COUNT(*) AS cnt FROM metrics")
	if err != nil {
		t.Fatal(err)
	}
	cnt := results[0]["cnt"].(int64)
	if cnt != 1 {
		t.Fatalf("expected 1 row, got %d", cnt)
	}
}

func TestQueryLiveRestrictedBlocksExternalFiles(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}

	secretPath := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}

	query := "SELECT content FROM read_text(" + SQLStringLiteral(secretPath) + ")"
	_, _, err := QueryLiveRestricted(dataDir, nil, nil, query)
	if err == nil {
		t.Fatal("expected external file read to fail")
	}
}

func TestQueryLiveAggregation(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	records := []source.Record{
		{Metric: "stars", ProjectID: "proj-a", Date: "2025-01-01", Value: 100},
		{Metric: "stars", ProjectID: "proj-a", Date: "2025-01-02", Value: 105},
		{Metric: "forks", ProjectID: "proj-a", Date: "2025-01-01", Value: 20},
		{Metric: "stars", ProjectID: "proj-b", Date: "2025-01-01", Value: 50},
	}
	if err := WriteRecords(dataDir, "pypi", "proj-a", records[:3]); err != nil {
		t.Fatal(err)
	}
	if err := WriteRecords(dataDir, "pypi", "proj-b", records[3:]); err != nil {
		t.Fatal(err)
	}

	results, _, err := QueryLive(dataDir, nil, nil, "SELECT project, SUM(value) AS total FROM metrics WHERE metric = 'stars' GROUP BY project ORDER BY project")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(results))
	}
	if results[0]["project"] != "proj-a" {
		t.Errorf("expected proj-a, got %v", results[0]["project"])
	}
}

func TestQueryLiveEmptyDir(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	_ = os.MkdirAll(dataDir, 0755)

	results, _, err := QueryLive(dataDir, nil, nil, "SELECT COUNT(*) AS cnt FROM metrics")
	if err != nil {
		t.Fatal(err)
	}
	cnt := results[0]["cnt"].(int64)
	if cnt != 0 {
		t.Fatalf("expected 0 rows, got %d", cnt)
	}
}

func TestQueryLiveInvalidSQL(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	_ = os.MkdirAll(dataDir, 0755)

	_, _, err := QueryLive(dataDir, nil, nil, "SELECT * FROM nonexistent_table")
	if err == nil {
		t.Fatal("expected error for invalid query")
	}
}

func TestSchemaLive(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	records := []source.Record{
		{Metric: "stars", ProjectID: "test", Date: "2025-01-01", Value: 1},
	}
	if err := WriteRecords(dataDir, "pypi", "test", records); err != nil {
		t.Fatal(err)
	}

	events := []source.Event{
		{Type: "star", ProjectID: "test", Target: "owner/repo", Datetime: "2025-01-01T10:00:00Z", Tags: map[string]string{"user": "alice"}},
	}
	if err := WriteEvents(dataDir, "github", "test", events); err != nil {
		t.Fatal(err)
	}

	cols, err := SchemaLive(dataDir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	eventsExpected := []string{"project", "source", "type", "target", "datetime", "ref", "tags"}
	metricsExpected := []string{"project", "source", "target", "metric", "date", "value", "tags"}
	projectsExpected := []string{"id", "name", "description", "color", "tags", "website", "logo"}

	var eventsCols, metricsCols, projectsCols []SchemaColumn
	for _, c := range cols {
		switch c.Table {
		case "events":
			eventsCols = append(eventsCols, c)
		case "metrics":
			metricsCols = append(metricsCols, c)
		case "projects":
			projectsCols = append(projectsCols, c)
		}
	}

	if len(eventsCols) != 7 {
		t.Fatalf("expected 7 events columns, got %d", len(eventsCols))
	}
	if len(metricsCols) != 7 {
		t.Fatalf("expected 7 metrics columns, got %d", len(metricsCols))
	}
	if len(projectsCols) != 7 {
		t.Fatalf("expected 7 projects columns, got %d", len(projectsCols))
	}

	for i, exp := range eventsExpected {
		if eventsCols[i].Column != exp {
			t.Errorf("events column %d: expected %s, got %s", i, exp, eventsCols[i].Column)
		}
	}

	for i, exp := range metricsExpected {
		if metricsCols[i].Column != exp {
			t.Errorf("metrics column %d: expected %s, got %s", i, exp, metricsCols[i].Column)
		}
	}

	for i, exp := range projectsExpected {
		if projectsCols[i].Column != exp {
			t.Errorf("projects column %d: expected %s, got %s", i, exp, projectsCols[i].Column)
		}
	}
}

func TestQueryLiveGitHubEvents(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	events := []source.Event{
		{Type: "star", ProjectID: "my-proj", Target: "owner/repo", Datetime: "2025-06-01T10:00:00Z", Tags: map[string]string{"user": "alice"}},
		{Type: "fork", ProjectID: "my-proj", Target: "owner/repo", Datetime: "2025-06-01T11:00:00Z", Tags: map[string]string{"user": "bob"}},
		{Type: "issue_open", ProjectID: "my-proj", Target: "owner/repo", Datetime: "2025-06-02T09:00:00Z", Tags: map[string]string{"user": "carol"}},
	}
	if err := WriteEvents(dataDir, "github", "my-proj", events); err != nil {
		t.Fatal(err)
	}

	results, _, err := QueryLive(dataDir, nil, nil, "SELECT COUNT(*) AS cnt FROM events")
	if err != nil {
		t.Fatal(err)
	}
	cnt := results[0]["cnt"].(int64)
	if cnt != 3 {
		t.Fatalf("expected 3 events, got %d", cnt)
	}

	results, _, err = QueryLive(dataDir, nil, nil, "SELECT type, tags->>'user' AS user FROM events WHERE type = 'star'")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 star event, got %d", len(results))
	}
	if results[0]["user"] != "alice" {
		t.Errorf("expected user=alice, got %v", results[0]["user"])
	}
}

func TestMetricsViewIncludesGitHubAggregated(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	records := []source.Record{
		{Metric: "downloads", ProjectID: "test", Date: "2025-06-01", Value: 10},
	}
	if err := WriteRecords(dataDir, "pypi", "test", records); err != nil {
		t.Fatal(err)
	}

	events := []source.Event{
		{Type: "star", ProjectID: "test", Target: "owner/repo", Datetime: "2025-06-01T10:00:00Z", Tags: map[string]string{"user": "alice"}},
	}
	if err := WriteEvents(dataDir, "github", "test", events); err != nil {
		t.Fatal(err)
	}

	results, _, err := QueryLive(dataDir, nil, nil, "SELECT COUNT(*) AS cnt FROM metrics")
	if err != nil {
		t.Fatal(err)
	}
	cnt := results[0]["cnt"].(int64)
	if cnt != 2 {
		t.Fatalf("expected 2 metric rows (1 pypi + 1 aggregated github event), got %d", cnt)
	}
}

func TestQueryLiveGitHubView(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	events := []source.Event{
		{Type: "star", ProjectID: "my-proj", Target: "owner/repo", Datetime: "2025-06-01T10:00:00Z", Tags: map[string]string{"user": "alice"}},
		{Type: "star", ProjectID: "my-proj", Target: "owner/repo", Datetime: "2025-06-01T12:00:00Z", Tags: map[string]string{"user": "bob"}},
		{Type: "fork", ProjectID: "my-proj", Target: "owner/repo", Datetime: "2025-06-01T11:00:00Z", Tags: map[string]string{"user": "carol"}},
	}
	if err := WriteEvents(dataDir, "github", "my-proj", events); err != nil {
		t.Fatal(err)
	}

	results, _, err := QueryLive(dataDir, nil, nil,
		"SELECT metric, value FROM metrics WHERE project = 'my-proj' AND source = 'github' AND date = '2025-06-01' ORDER BY metric")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(results))
	}
	if results[0]["metric"] != "daily_forks" || results[0]["value"].(int64) != 1 {
		t.Errorf("unexpected fork row: %v", results[0])
	}
	if results[1]["metric"] != "daily_stars" || results[1]["value"].(int64) != 2 {
		t.Errorf("unexpected star row: %v", results[1])
	}
}

func TestMetricsFilledForwardFills(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	// Write total_downloads on day 1 and day 4 (simulating suppressed days 2-3)
	records1 := []source.Record{
		{Metric: "total_downloads", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-01", Value: 100},
	}
	if err := WriteRecords(dataDir, "openvsx", "proj", records1); err != nil {
		t.Fatal(err)
	}

	records4 := []source.Record{
		{Metric: "total_downloads", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-04", Value: 110},
	}
	if err := WriteRecords(dataDir, "openvsx", "proj", records4); err != nil {
		t.Fatal(err)
	}

	// Raw metrics should only have 2 rows
	results, _, err := QueryLive(dataDir, nil, nil,
		"SELECT COUNT(*) AS cnt FROM metrics WHERE metric = 'total_downloads'")
	if err != nil {
		t.Fatal(err)
	}
	if results[0]["cnt"].(int64) != 2 {
		t.Fatalf("expected 2 raw rows, got %d", results[0]["cnt"])
	}

	// metrics_filled should have 4 rows (days 1-4, forward-filled)
	results, _, err = QueryLive(dataDir, nil, nil,
		"SELECT date, value FROM metrics_filled WHERE metric = 'total_downloads' ORDER BY date")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 {
		t.Fatalf("expected 4 filled rows, got %d", len(results))
	}

	expected := []struct {
		date  string
		value int64
	}{
		{"2025-06-01", 100},
		{"2025-06-02", 100},
		{"2025-06-03", 100},
		{"2025-06-04", 110},
	}
	for i, exp := range expected {
		v := results[i]["value"].(int64)
		if v != exp.value {
			t.Errorf("day %s: got value %d, want %d", exp.date, v, exp.value)
		}
	}
}

func TestMetricsFilledUsesWatermarkHorizon(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	records1 := []source.Record{
		{Metric: "total_downloads", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-01", Value: 100},
	}
	if err := WriteRecords(dataDir, "openvsx", "proj", records1); err != nil {
		t.Fatal(err)
	}

	records2 := []source.Record{
		{Metric: "total_downloads", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-02", Value: 100},
		{Metric: "total_downloads", ProjectID: "proj", Target: "ns/ext", Date: "2025-06-03", Value: 100},
	}
	if err := WriteRecords(dataDir, "openvsx", "proj", records2); err != nil {
		t.Fatal(err)
	}

	results, _, err := QueryLive(dataDir, nil, nil,
		"SELECT COUNT(*) AS cnt FROM metrics WHERE metric = 'total_downloads'")
	if err != nil {
		t.Fatal(err)
	}
	if results[0]["cnt"].(int64) != 1 {
		t.Fatalf("expected 1 raw row, got %d", results[0]["cnt"])
	}

	results, _, err = QueryLive(dataDir, nil, nil,
		"SELECT date, value FROM metrics_filled WHERE metric = 'total_downloads' ORDER BY date")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 filled rows through watermark, got %d", len(results))
	}
	for _, row := range results {
		if row["value"].(int64) != 100 {
			t.Errorf("expected filled value 100, got %v", row)
		}
	}
}

func TestMetricsFilledUsesWatermarkSeriesKey(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	records1 := []source.Record{
		{Metric: "total_downloads", ProjectID: "proj", Target: "ns/current", Date: "2025-06-01", Value: 100},
		{Metric: "total_downloads", ProjectID: "proj", Target: "ns/removed", Date: "2025-06-01", Value: 200},
	}
	if err := WriteRecords(dataDir, "openvsx", "proj", records1); err != nil {
		t.Fatal(err)
	}

	records2 := []source.Record{
		{Metric: "total_downloads", ProjectID: "proj", Target: "ns/current", Date: "2025-06-02", Value: 100},
	}
	if err := WriteRecords(dataDir, "openvsx", "proj", records2); err != nil {
		t.Fatal(err)
	}

	results, _, err := QueryLive(dataDir, nil, nil,
		"SELECT target, date, value FROM metrics_filled WHERE metric = 'total_downloads' ORDER BY target, date")
	if err != nil {
		t.Fatal(err)
	}

	want := []struct {
		target string
		date   string
		value  int64
	}{
		{"ns/current", "2025-06-01", 100},
		{"ns/current", "2025-06-02", 100},
		{"ns/removed", "2025-06-01", 200},
	}
	if len(results) != len(want) {
		t.Fatalf("expected %d filled rows, got %d: %v", len(want), len(results), results)
	}
	for i, exp := range want {
		date := results[i]["date"].(time.Time).Format("2006-01-02")
		if results[i]["target"] != exp.target || date != exp.date || results[i]["value"].(int64) != exp.value {
			t.Fatalf("row %d = %v, want target=%s date=%s value=%d", i, results[i], exp.target, exp.date, exp.value)
		}
	}
}

// Series that share a target advance to that target's watermark horizon, even
// after a series stops being reported. Watermarks are stored one row per target
// (all a channel's videos are fetched together), so a removed video forward-fills
// at its last value through the target's latest fetch date.
func TestMetricsFilledFillsRemovedSeriesToTargetHorizon(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	records1 := []source.Record{
		{Metric: "total_views", ProjectID: "proj", Target: "@chan", Date: "2025-06-01", Value: 100, Tags: map[string]string{"video_id": "current"}},
		{Metric: "total_views", ProjectID: "proj", Target: "@chan", Date: "2025-06-01", Value: 200, Tags: map[string]string{"video_id": "removed"}},
	}
	if err := WriteRecords(dataDir, "youtube", "proj", records1); err != nil {
		t.Fatal(err)
	}

	records2 := []source.Record{
		{Metric: "total_views", ProjectID: "proj", Target: "@chan", Date: "2025-06-02", Value: 100, Tags: map[string]string{"video_id": "current"}},
	}
	if err := WriteRecords(dataDir, "youtube", "proj", records2); err != nil {
		t.Fatal(err)
	}

	results, _, err := QueryLive(dataDir, nil, nil,
		"SELECT tags->>'video_id' AS video_id, date, value FROM metrics_filled WHERE metric = 'total_views' ORDER BY video_id, date")
	if err != nil {
		t.Fatal(err)
	}

	want := []struct {
		videoID string
		date    string
		value   int64
	}{
		{"current", "2025-06-01", 100},
		{"current", "2025-06-02", 100},
		{"removed", "2025-06-01", 200},
		{"removed", "2025-06-02", 200},
	}
	if len(results) != len(want) {
		t.Fatalf("expected %d filled rows, got %d: %v", len(want), len(results), results)
	}
	for i, exp := range want {
		date := results[i]["date"].(time.Time).Format("2006-01-02")
		if results[i]["video_id"] != exp.videoID || date != exp.date || results[i]["value"].(int64) != exp.value {
			t.Fatalf("row %d = %v, want video_id=%s date=%s value=%d", i, results[i], exp.videoID, exp.date, exp.value)
		}
	}
}

func TestMetricsFilledPassesThroughDailyMetrics(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	records := []source.Record{
		{Metric: "daily_downloads", ProjectID: "proj", Target: "pkg", Date: "2025-06-01", Value: 50},
		{Metric: "daily_downloads", ProjectID: "proj", Target: "pkg", Date: "2025-06-03", Value: 75},
	}
	if err := WriteRecords(dataDir, "pypi", "proj", records); err != nil {
		t.Fatal(err)
	}

	// daily metrics should NOT be forward-filled (only 2 rows)
	results, _, err := QueryLive(dataDir, nil, nil,
		"SELECT COUNT(*) AS cnt FROM metrics_filled WHERE metric = 'daily_downloads'")
	if err != nil {
		t.Fatal(err)
	}
	if results[0]["cnt"].(int64) != 2 {
		t.Fatalf("expected 2 daily rows (no fill), got %d", results[0]["cnt"])
	}
}

func TestMetricsFilledEmptyDB(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	_ = os.MkdirAll(dataDir, 0755)

	results, _, err := QueryLive(dataDir, nil, nil, "SELECT COUNT(*) AS cnt FROM metrics_filled")
	if err != nil {
		t.Fatal(err)
	}
	if results[0]["cnt"].(int64) != 0 {
		t.Fatalf("expected 0 rows, got %d", results[0]["cnt"])
	}
}
