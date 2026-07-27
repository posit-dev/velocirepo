package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSchemaVersionMissing(t *testing.T) {
	dir := t.TempDir()
	v, err := SchemaVersion(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v != 0 {
		t.Errorf("expected version 0 for missing file, got %d", v)
	}
}

func TestSchemaVersionReadWrite(t *testing.T) {
	dir := t.TempDir()
	if err := writeSchemaVersion(dir, 3); err != nil {
		t.Fatal(err)
	}
	v, err := SchemaVersion(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v != 3 {
		t.Errorf("expected version 3, got %d", v)
	}
}

func TestCheckSchemaVersionNoDataDir(t *testing.T) {
	err := CheckSchemaVersion("/nonexistent/path")
	if err != nil {
		t.Errorf("expected nil for nonexistent dir, got: %v", err)
	}
}

func TestCheckSchemaVersionStale(t *testing.T) {
	dir := t.TempDir()
	_ = writeSchemaVersion(dir, 0)
	err := CheckSchemaVersion(dir)
	if err == nil {
		t.Fatal("expected error for stale schema")
	}
}

func TestCheckSchemaVersionCurrent(t *testing.T) {
	dir := t.TempDir()
	_ = writeSchemaVersion(dir, LatestSchemaVersion)
	err := CheckSchemaVersion(dir)
	if err != nil {
		t.Errorf("expected nil for current schema, got: %v", err)
	}
}

func TestCheckSchemaVersionFuture(t *testing.T) {
	dir := t.TempDir()
	_ = writeSchemaVersion(dir, LatestSchemaVersion+1)
	err := CheckSchemaVersion(dir)
	if err == nil {
		t.Fatal("expected error for future schema")
	}
}

func TestMigrate0to1(t *testing.T) {
	dir := t.TempDir()

	// Set up pypi data with old metric names
	writeTestRaw(t, filepath.Join(dir, "pypi", "myproj", "2025-06-01.jsonl"), []string{
		`{"source":"pypi","metric":"downloads","project_id":"myproj","date":"2025-06-01","value":100}`,
	})

	// Set up plausible data
	writeTestRaw(t, filepath.Join(dir, "plausible", "myproj", "2025-06-01.jsonl"), []string{
		`{"source":"plausible","metric":"pageviews","project_id":"myproj","date":"2025-06-01","value":500}`,
		`{"source":"plausible","metric":"visitors","project_id":"myproj","date":"2025-06-01","value":200}`,
	})

	// Set up openvsx data
	writeTestRaw(t, filepath.Join(dir, "openvsx", "myproj", "2025-06-01.jsonl"), []string{
		`{"source":"openvsx","metric":"total_downloads","project_id":"myproj","date":"2025-06-01","value":5000}`,
		`{"source":"openvsx","metric":"reviews","project_id":"myproj","date":"2025-06-01","value":10}`,
		`{"source":"openvsx","metric":"rating","project_id":"myproj","date":"2025-06-01","value":450}`,
	})

	applied, err := Migrate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 7 {
		t.Errorf("expected 7 migrations applied, got %d", applied)
	}

	v, _ := SchemaVersion(dir)
	if v != LatestSchemaVersion {
		t.Errorf("expected schema version %d, got %d", LatestSchemaVersion, v)
	}

	// Verify pypi (v4 moves it into metrics/)
	data, _ := os.ReadFile(metricsPath(dir, "pypi", "myproj", "2025-06-01"))
	if got := string(data); !contains(got, `"daily_downloads"`) {
		t.Errorf("pypi not migrated: %s", got)
	}

	// Verify plausible (v4 moves it into metrics/)
	data, _ = os.ReadFile(metricsPath(dir, "plausible", "myproj", "2025-06-01"))
	content := string(data)
	if !contains(content, `"daily_site_pageviews"`) || !contains(content, `"daily_site_visitors"`) {
		t.Errorf("plausible not migrated: %s", content)
	}

	// Verify openvsx (v4 moves it into metrics/)
	data, _ = os.ReadFile(metricsPath(dir, "openvsx", "myproj", "2025-06-01"))
	content = string(data)
	if !contains(content, `"total_downloads"`) || !contains(content, `"total_reviews"`) || !contains(content, `"total_ratings"`) {
		t.Errorf("openvsx not migrated: %s", content)
	}
}

func TestMigrate4to5YouTubeIndex(t *testing.T) {
	dir := t.TempDir()
	_ = writeSchemaVersion(dir, 4)

	// Set up a YouTube index in the old location
	oldIndexPath := metricsPath(dir, "youtube", "my-proj", "index")
	writeTestRaw(t, oldIndexPath, []string{
		`{"video_id":"abc123","title":"Test Video","published_at":"2025-06-01T10:00:00Z","channel":"@TestChan","duration":330,"tags":["go","tutorial"]}`,
		`{"video_id":"def456","title":"Second Video","published_at":"2025-07-01T10:00:00Z","channel":"@TestChan","duration":600}`,
		`{"video_id":"ghi789","title":"Livestream","published_at":"2025-08-01T10:00:00Z","channel":"@TestChan","duration":0}`,
	})

	// Seeded at v4, so Migrate runs 4→5, 5→6, and 6→7.
	applied, err := Migrate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 3 {
		t.Errorf("expected 3 migrations applied, got %d", applied)
	}

	// Old file should be removed
	if _, err := os.Stat(oldIndexPath); !os.IsNotExist(err) {
		t.Error("old index.jsonl should have been removed")
	}

	// New file should exist
	newPath := contentPath(dir, "youtube", "my-proj", "videos")
	data, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("new content file not created: %v", err)
	}

	content := string(data)
	if !contains(content, `"source":"youtube"`) {
		t.Error("expected source=youtube in migrated content")
	}
	if !contains(content, `"id":"abc123"`) {
		t.Error("expected id=abc123 in migrated content")
	}
	if !contains(content, `"target":"@TestChan"`) {
		t.Error("expected target=@TestChan in migrated content")
	}
	if !contains(content, `"url":"https://youtube.com/watch?v=abc123"`) {
		t.Error("expected url for abc123 in migrated content")
	}
	if !contains(content, `"type":"video"`) {
		t.Error("expected type=video in migrated content")
	}
	if !contains(content, `"duration":330`) {
		t.Error("expected duration=330 in migrated content")
	}
	if !contains(content, `"id":"ghi789"`) {
		t.Error("expected id=ghi789 in migrated content")
	}
	if contains(content, `"duration":0`) {
		t.Error("zero duration should be omitted, not written as 0")
	}
}

func TestMigrate5to6CollapsesWatermarks(t *testing.T) {
	dir := t.TempDir()
	_ = writeSchemaVersion(dir, 5)

	// Old-format watermarks: dated *.jsonl files under data/watermarks/metrics/,
	// one row per total-series key. Two targets, each with entries on two dates.
	oldDir := filepath.Join(dir, "watermarks", "metrics", "youtube", "my-proj")
	writeTestRaw(t, filepath.Join(oldDir, "2025-06-01.jsonl"), []string{
		`{"source":"youtube","metric":"total_views","project_id":"my-proj","target":"vidA","date":"2025-06-01","tags":{"video_id":"vidA"}}`,
		`{"source":"youtube","metric":"total_likes","project_id":"my-proj","target":"vidA","date":"2025-06-01","tags":{"video_id":"vidA"}}`,
		`{"source":"youtube","metric":"total_views","project_id":"my-proj","target":"vidB","date":"2025-06-01","tags":{"video_id":"vidB"}}`,
	})
	writeTestRaw(t, filepath.Join(oldDir, "2025-06-02.jsonl"), []string{
		`{"source":"youtube","metric":"total_views","project_id":"my-proj","target":"vidA","date":"2025-06-02","tags":{"video_id":"vidA"}}`,
	})

	applied, err := Migrate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 2 {
		t.Errorf("expected 2 migrations applied, got %d", applied)
	}

	// Old tree is removed entirely.
	if _, err := os.Stat(filepath.Join(dir, "watermarks")); !os.IsNotExist(err) {
		t.Error("old watermarks tree should have been removed")
	}

	// New collapsed file exists, one entry per target at its latest date.
	newPath := WatermarkFilePath(dir, "youtube", "my-proj")
	data, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("collapsed watermark file not created: %v", err)
	}
	watermarks, err := readMetricWatermarks(newPath)
	if err != nil {
		t.Fatalf("read collapsed watermarks: %v", err)
	}
	if len(watermarks) != 2 {
		t.Fatalf("expected 2 target entries, got %d: %s", len(watermarks), data)
	}
	byTarget := map[string]string{}
	for _, w := range watermarks {
		byTarget[w.Target] = w.Date
	}
	if byTarget["vidA"] != "2025-06-02" {
		t.Errorf("vidA should advance to latest date 2025-06-02, got %q", byTarget["vidA"])
	}
	if byTarget["vidB"] != "2025-06-01" {
		t.Errorf("vidB should be 2025-06-01, got %q", byTarget["vidB"])
	}
	// The pre-v6 per-series fields (metric, tags) must not survive the collapse.
	if contains(string(data), `"metric"`) || contains(string(data), `"tags"`) {
		t.Errorf("collapsed watermark should not carry metric/tags fields: %s", data)
	}
}

func TestMigrate5to6NoWatermarksTree(t *testing.T) {
	dir := t.TempDir()
	_ = writeSchemaVersion(dir, 5)

	applied, err := Migrate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 2 {
		t.Errorf("expected 2 migrations applied, got %d", applied)
	}
	v, _ := SchemaVersion(dir)
	if v != LatestSchemaVersion {
		t.Errorf("expected schema version %d, got %d", LatestSchemaVersion, v)
	}
}

func TestMigrate5to6DoesNotRegressNewerExistingWatermark(t *testing.T) {
	dir := t.TempDir()
	_ = writeSchemaVersion(dir, 5)

	// A v5 data dir can already hold a collapsed _watermark.json (written by the
	// pre-v6 code) with a newer date than a leftover old tree entry for the same
	// target. Migration must keep the newer date, not overwrite it with the old.
	existingPath := WatermarkFilePath(dir, "youtube", "my-proj")
	writeTestRaw(t, existingPath, []string{
		`{"source":"youtube","project_id":"my-proj","target":"vidA","date":"2025-07-10"}`,
	})

	oldDir := filepath.Join(dir, "watermarks", "metrics", "youtube", "my-proj")
	writeTestRaw(t, filepath.Join(oldDir, "2025-06-01.jsonl"), []string{
		`{"source":"youtube","metric":"total_views","project_id":"my-proj","target":"vidA","date":"2025-06-01"}`,
		`{"source":"youtube","metric":"total_views","project_id":"my-proj","target":"vidB","date":"2025-06-01"}`,
	})

	if _, err := Migrate(dir); err != nil {
		t.Fatal(err)
	}

	watermarks, err := readMetricWatermarks(existingPath)
	if err != nil {
		t.Fatalf("read merged watermarks: %v", err)
	}
	byTarget := map[string]string{}
	for _, w := range watermarks {
		byTarget[w.Target] = w.Date
	}
	// vidA keeps its newer existing date; vidB is carried over from the old tree.
	if byTarget["vidA"] != "2025-07-10" {
		t.Errorf("vidA should keep newer date 2025-07-10, got %q", byTarget["vidA"])
	}
	if byTarget["vidB"] != "2025-06-01" {
		t.Errorf("vidB should be migrated from old tree as 2025-06-01, got %q", byTarget["vidB"])
	}
	if _, err := os.Stat(filepath.Join(dir, "watermarks")); !os.IsNotExist(err) {
		t.Error("old watermarks tree should have been removed")
	}
}

func TestMigrateAlreadyCurrent(t *testing.T) {
	dir := t.TempDir()
	_ = writeSchemaVersion(dir, LatestSchemaVersion)

	applied, err := Migrate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Errorf("expected 0 migrations applied, got %d", applied)
	}
}

func TestEnsureSchemaVersion(t *testing.T) {
	dir := t.TempDir()

	ensureSchemaVersion(dir)
	v, _ := SchemaVersion(dir)
	if v != LatestSchemaVersion {
		t.Errorf("expected version %d, got %d", LatestSchemaVersion, v)
	}

	// Should not overwrite existing version
	_ = writeSchemaVersion(dir, 0)
	ensureSchemaVersion(dir)
	v, _ = SchemaVersion(dir)
	if v != 0 {
		t.Errorf("expected version 0 (not overwritten), got %d", v)
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (len(s) >= len(substr)) && (s != "" && substr != "") && (indexOf(s, substr) >= 0)
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
