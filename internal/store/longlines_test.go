package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// JSONL lines can be longer than bufio.Scanner's 1 MB limit: an RSS post
// stored as content, or a metric with a long extra value.
func longLine(t *testing.T, fields map[string]any) string {
	t.Helper()
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 2<<20 {
		t.Fatalf("line is only %d bytes", len(data))
	}
	return string(data)
}

func TestValidateReadsPastLongLines(t *testing.T) {
	dir := t.TempDir()
	path := metricsPath(dir, "pypi", "proj", "2025-06-01")
	writeTestRaw(t, path, []string{
		longLine(t, map[string]any{"source": "pypi", "metric": "daily_downloads", "project_id": "proj", "target": "pkg", "date": "2025-06-01", "value": 1, "extra": map[string]string{"note": strings.Repeat("x", 3<<20)}}),
		`{not json`,
	})

	result, err := ValidateData(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range result.Issues {
		if issue.Type == IssueMalformedJSON && issue.Line == 2 {
			return
		}
	}
	t.Fatalf("the malformed line after a long line was not reported; issues: %v", result.Issues)
}

func TestRewriteProjectIDHandlesLongLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "posts.jsonl")
	writeTestRaw(t, path, []string{
		longLine(t, map[string]any{"source": "rss", "project_id": "old", "id": "1", "content": strings.Repeat("y", 3<<20)}),
	})

	if err := RewriteProjectID(dir, "old", "new"); err != nil {
		t.Fatalf("RewriteProjectID: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"project_id":"new"`) {
		t.Errorf("project_id was not rewritten")
	}
}

func TestMigrationHandlesLongLines(t *testing.T) {
	dir := t.TempDir()
	path := metricsPath(dir, "youtube", "proj", "2025-06-01")
	writeTestRaw(t, path, []string{
		longLine(t, map[string]any{"source": "youtube", "metric": "total_views", "project_id": "proj", "date": "2025-06-01", "value": 1, "tags": map[string]string{"title": strings.Repeat("z", 3<<20)}}),
	})

	if err := migrate6to7(dir); err != nil {
		t.Fatalf("migrate6to7: %v", err)
	}
	got, err := ReadRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Extra["title"]) != 3<<20 {
		t.Fatalf("tags were not moved to extra: %d records", len(got))
	}
}
