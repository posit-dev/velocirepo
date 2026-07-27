package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/posit-dev/velocirepo/internal/source"
)

func TestValidateData_Clean(t *testing.T) {
	dataDir := testDataDir(t)

	writeTestRecords(t, metricsPath(dataDir, "pypi", "myproj", "2025-06-01"),
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "myproj", Target: "myproj", Date: "2025-06-01", Value: 100},
	)

	projects := map[string]bool{"myproj": true}
	result, err := ValidateData(dataDir, projects)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Issues) != 0 {
		t.Errorf("expected 0 issues, got %d: %v", len(result.Issues), result.Issues)
	}
	if result.FilesRead != 1 {
		t.Errorf("expected 1 file read, got %d", result.FilesRead)
	}
	if result.RecordCount != 1 {
		t.Errorf("expected 1 record, got %d", result.RecordCount)
	}
}

func TestValidateData_MalformedJSON(t *testing.T) {
	dataDir := testDataDir(t)

	writeTestRaw(t, metricsPath(dataDir, "pypi", "myproj", "2025-06-01"), []string{
		`{"source":"pypi","metric":"daily_downloads","project_id":"myproj","target":"myproj","date":"2025-06-01","value":100}`,
		`{invalid json`,
		`{"source":"pypi","metric":"total_downloads","project_id":"myproj","target":"myproj","date":"2025-06-01","value":200}`,
	})

	projects := map[string]bool{"myproj": true}
	result, err := ValidateData(dataDir, projects)
	if err != nil {
		t.Fatal(err)
	}

	assertIssueTypes(t, result.Issues, IssueMalformedJSON)
	if result.Issues[0].Line != 2 {
		t.Errorf("expected line 2, got %d", result.Issues[0].Line)
	}
	if !result.Issues[0].Fixable {
		t.Error("expected issue to be fixable")
	}
}

func TestValidateData_InvalidDate(t *testing.T) {
	dataDir := testDataDir(t)

	writeTestRecords(t, metricsPath(dataDir, "pypi", "myproj", "2025-06-01"),
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "myproj", Target: "myproj", Date: "2025-6-1", Value: 100},
	)

	projects := map[string]bool{"myproj": true}
	result, err := ValidateData(dataDir, projects)
	if err != nil {
		t.Fatal(err)
	}

	assertIssueTypes(t, result.Issues, IssueInvalidDate)
}

func TestValidateData_DateMismatch(t *testing.T) {
	dataDir := testDataDir(t)

	writeTestRecords(t, metricsPath(dataDir, "pypi", "myproj", "2025-06-01"),
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "myproj", Target: "myproj", Date: "2025-06-01", Value: 100},
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "myproj", Target: "myproj", Date: "2025-06-02", Value: 200},
	)

	projects := map[string]bool{"myproj": true}
	result, err := ValidateData(dataDir, projects)
	if err != nil {
		t.Fatal(err)
	}

	assertIssueTypes(t, result.Issues, IssueDateMismatch)
	if !result.Issues[0].Fixable {
		t.Error("expected issue to be fixable")
	}
}

func TestValidateData_DateMismatch_Monthly(t *testing.T) {
	dataDir := testDataDir(t)

	writeTestRecords(t, metricsPath(dataDir, "pypi", "myproj", "2025-06"),
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "myproj", Target: "myproj", Date: "2025-06-15", Value: 100},
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "myproj", Target: "myproj", Date: "2025-07-01", Value: 200},
	)

	projects := map[string]bool{"myproj": true}
	result, err := ValidateData(dataDir, projects)
	if err != nil {
		t.Fatal(err)
	}

	assertIssueTypes(t, result.Issues, IssueDateMismatch)
}

func TestValidateData_EmptyFields(t *testing.T) {
	dataDir := testDataDir(t)

	writeTestRecords(t, metricsPath(dataDir, "pypi", "myproj", "2025-06-01"),
		source.Record{Source: "pypi", Metric: "", ProjectID: "myproj", Target: "myproj", Date: "2025-06-01", Value: 100},
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "", Target: "myproj", Date: "2025-06-01", Value: 200},
	)

	projects := map[string]bool{"myproj": true}
	result, err := ValidateData(dataDir, projects)
	if err != nil {
		t.Fatal(err)
	}

	assertIssueTypes(t, result.Issues, IssueEmptyField, IssueEmptyField)
}

func TestValidateData_ContentUpdatedAtOnly(t *testing.T) {
	dataDir := testDataDir(t)

	// An RSS person entry with only updated_at (no published_at) is valid.
	writeTestRaw(t, contentPath(dataDir, "rss", "myproj", "people"), []string{
		`{"source":"rss","project_id":"myproj","target":"https://example.com/people.xml","id":"p1","title":"Jane","updated_at":"2026-05-21T18:03:11Z","type":"person"}`,
	})

	projects := map[string]bool{"myproj": true}
	result, err := ValidateData(dataDir, projects)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Issues) != 0 {
		t.Errorf("expected 0 issues for updated_at-only entry, got %d: %v", len(result.Issues), result.Issues)
	}
}

func TestValidateData_ContentNoTimestamp(t *testing.T) {
	dataDir := testDataDir(t)

	// Neither published_at nor updated_at → empty-field issue.
	writeTestRaw(t, contentPath(dataDir, "rss", "myproj", "people"), []string{
		`{"source":"rss","project_id":"myproj","target":"https://example.com/people.xml","id":"p1","title":"Jane","type":"person"}`,
	})

	projects := map[string]bool{"myproj": true}
	result, err := ValidateData(dataDir, projects)
	if err != nil {
		t.Fatal(err)
	}

	assertIssueTypes(t, result.Issues, IssueEmptyField)
}

func TestValidateData_ContentInvalidUpdatedAt(t *testing.T) {
	dataDir := testDataDir(t)

	// A malformed updated_at is still flagged even when published_at is absent.
	writeTestRaw(t, contentPath(dataDir, "rss", "myproj", "people"), []string{
		`{"source":"rss","project_id":"myproj","target":"https://example.com/people.xml","id":"p1","title":"Jane","updated_at":"not-a-date","type":"person"}`,
	})

	projects := map[string]bool{"myproj": true}
	result, err := ValidateData(dataDir, projects)
	if err != nil {
		t.Fatal(err)
	}

	assertIssueTypes(t, result.Issues, IssueInvalidDatetime)
}

func TestValidateData_Duplicates(t *testing.T) {
	dataDir := testDataDir(t)

	writeTestRecords(t, metricsPath(dataDir, "pypi", "myproj", "2025-06-01"),
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "myproj", Target: "myproj", Date: "2025-06-01", Value: 100},
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "myproj", Target: "myproj", Date: "2025-06-01", Value: 100},
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "myproj", Target: "myproj", Date: "2025-06-01", Value: 200},
	)

	projects := map[string]bool{"myproj": true}
	result, err := ValidateData(dataDir, projects)
	if err != nil {
		t.Fatal(err)
	}

	assertIssueTypes(t, result.Issues, IssueDuplicate, IssueDuplicate)
}

func TestValidateData_OrphanDir(t *testing.T) {
	dataDir := testDataDir(t)

	writeTestRecords(t, metricsPath(dataDir, "pypi", "unknown-proj", "2025-06-01"),
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "unknown-proj", Target: "x", Date: "2025-06-01", Value: 100},
	)

	projects := map[string]bool{"myproj": true}
	result, err := ValidateData(dataDir, projects)
	if err != nil {
		t.Fatal(err)
	}

	assertIssueTypes(t, result.Issues, IssueOrphanDir)
	if !result.Issues[0].Fixable {
		t.Error("expected issue to be fixable")
	}
}

func TestValidateData_GitHubEvents(t *testing.T) {
	dataDir := testDataDir(t)

	writeTestEvents(t, eventsPath(dataDir, "github", "myproj", "2025-06-01"),
		source.Event{Source: "github", Type: "star", ProjectID: "myproj", Target: "owner/repo", Datetime: "2025-06-01T10:00:00Z", User: "alice"},
		source.Event{Source: "github", Type: "star", ProjectID: "myproj", Target: "owner/repo", Datetime: "2025-06-01T10:00:00Z", User: "alice"},
	)

	projects := map[string]bool{"myproj": true}
	result, err := ValidateData(dataDir, projects)
	if err != nil {
		t.Fatal(err)
	}

	assertIssueTypes(t, result.Issues, IssueDuplicate)
}

func TestValidateData_GitHubEvents_InvalidDatetime(t *testing.T) {
	dataDir := testDataDir(t)

	writeTestEvents(t, eventsPath(dataDir, "github", "myproj", "2025-06-01"),
		source.Event{Source: "github", Type: "star", ProjectID: "myproj", Target: "owner/repo", Datetime: "bad", User: "alice"},
	)

	projects := map[string]bool{"myproj": true}
	result, err := ValidateData(dataDir, projects)
	if err != nil {
		t.Fatal(err)
	}

	assertIssueTypes(t, result.Issues, IssueInvalidDatetime)
}

func TestValidateData_UnexpectedFile(t *testing.T) {
	dataDir := testDataDir(t)

	writeTestRaw(t, filepath.Join(dataDir, "metrics", "pypi", "myproj", "notes.txt"), []string{"hello"})

	projects := map[string]bool{"myproj": true}
	result, err := ValidateData(dataDir, projects)
	if err != nil {
		t.Fatal(err)
	}

	assertIssueTypes(t, result.Issues, IssueUnexpectedFile)
}

func TestFixMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	writeTestRaw(t, path, []string{
		`{"source":"pypi","metric":"x","project_id":"p","date":"2025-01-01","value":1}`,
		`{bad`,
		`{"source":"pypi","metric":"y","project_id":"p","date":"2025-01-01","value":2}`,
	})

	result := FixMalformedJSON(map[string][]int{path: {2}})
	if result.Fixed != 1 {
		t.Errorf("expected 1 fixed, got %d", result.Fixed)
	}

	records, err := ReadRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Errorf("expected 2 records after fix, got %d", len(records))
	}
}

func TestFixDuplicates(t *testing.T) {
	dataDir := testDataDir(t)
	path := metricsPath(dataDir, "pypi", "myproj", "2025-06-01")

	writeTestRecords(t, path,
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "myproj", Target: "myproj", Date: "2025-06-01", Value: 100},
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "myproj", Target: "myproj", Date: "2025-06-01", Value: 100},
	)

	result := FixDuplicates([]string{path})
	if result.Fixed != 1 {
		t.Errorf("expected 1 fixed, got %d", result.Fixed)
	}

	records, err := ReadRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Errorf("expected 1 record after fix, got %d", len(records))
	}
}

func TestFixDateMismatches(t *testing.T) {
	dataDir := testDataDir(t)
	path := metricsPath(dataDir, "pypi", "myproj", "2025-06-01")

	writeTestRecords(t, path,
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "myproj", Target: "myproj", Date: "2025-06-01", Value: 100},
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "myproj", Target: "myproj", Date: "2025-06-02", Value: 200},
	)

	result := FixDateMismatches([]string{path})
	if result.Fixed != 1 {
		t.Errorf("expected 1 fixed, got %d", result.Fixed)
	}

	records, err := ReadRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Errorf("expected 1 record in original file, got %d", len(records))
	}
	if records[0].Date != "2025-06-01" {
		t.Errorf("expected date 2025-06-01, got %s", records[0].Date)
	}

	movedPath := metricsPath(dataDir, "pypi", "myproj", "2025-06-02")
	movedRecords, err := ReadRecords(movedPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(movedRecords) != 1 {
		t.Errorf("expected 1 record in new file, got %d", len(movedRecords))
	}
	if movedRecords[0].Date != "2025-06-02" {
		t.Errorf("expected date 2025-06-02, got %s", movedRecords[0].Date)
	}
}

func TestFixOrphanDirs(t *testing.T) {
	dir := t.TempDir()
	orphanPath := filepath.Join(dir, "data", "metrics", "pypi", "orphan")
	_ = os.MkdirAll(orphanPath, 0755)

	result := FixOrphanDirs([]string{orphanPath})
	if result.Fixed != 1 {
		t.Errorf("expected 1 fixed, got %d", result.Fixed)
	}

	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Error("expected orphan dir to be removed")
	}
}

func TestFixSourceMismatches(t *testing.T) {
	dataDir := testDataDir(t)

	// File at .../content/youtube/myproj/videos.jsonl → sourceName = "youtube"
	path := contentPath(dataDir, "youtube", "myproj", "videos")

	writeTestRaw(t, path, []string{
		`{"source":"github","id":"v1","project_id":"myproj","name":"Video 1"}`,
		`{"source":"youtube","id":"v2","project_id":"myproj","name":"Video 2"}`,
		`{"id":"v3","project_id":"myproj","name":"Video 3"}`,
	})

	result := FixSourceMismatches([]string{path})
	if result.Fixed != 1 {
		t.Errorf("expected 1 fixed, got %d", result.Fixed)
	}

	lines := readRawLines(t, path)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}

	// First record: source rewritten from "github" to "youtube"
	var rec0 map[string]interface{}
	_ = json.Unmarshal([]byte(lines[0]), &rec0)
	if rec0["source"] != "youtube" {
		t.Errorf("expected source youtube, got %v", rec0["source"])
	}

	// Second record: already correct, unchanged
	var rec1 map[string]interface{}
	_ = json.Unmarshal([]byte(lines[1]), &rec1)
	if rec1["source"] != "youtube" {
		t.Errorf("expected source youtube, got %v", rec1["source"])
	}

	// Third record: empty source should NOT be rewritten
	var rec2 map[string]interface{}
	_ = json.Unmarshal([]byte(lines[2]), &rec2)
	if _, ok := rec2["source"]; ok {
		t.Errorf("expected no source field, got %v", rec2["source"])
	}
}

func TestFixDeprecatedMetrics(t *testing.T) {
	dataDir := testDataDir(t)

	path := metricsPath(dataDir, "openvsx", "myproj", "2026-01-01")
	writeTestRaw(t, path, []string{
		`{"metric":"total_downloads","project_id":"myproj","date":"2026-01-01","value":1000,"source":"openvsx","target":"ns/ext"}`,
		`{"metric":"rating","project_id":"myproj","date":"2026-01-01","value":5.0,"source":"openvsx","target":"ns/ext"}`,
		`{"metric":"reviews","project_id":"myproj","date":"2026-01-01","value":2,"source":"openvsx","target":"ns/ext"}`,
		`{"metric":"rating","project_id":"myproj","date":"2026-01-01","value":450,"source":"openvsx","target":"ns/ext"}`,
	})

	result := FixDeprecatedMetrics([]string{path})
	if result.Fixed != 3 {
		t.Errorf("expected 3 fixed, got %d", result.Fixed)
	}

	lines := readRawLines(t, path)
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d", len(lines))
	}

	var rec0 map[string]interface{}
	_ = json.Unmarshal([]byte(lines[0]), &rec0)
	if rec0["metric"] != "total_downloads" {
		t.Errorf("expected total_downloads unchanged, got %v", rec0["metric"])
	}

	var rec1 map[string]interface{}
	_ = json.Unmarshal([]byte(lines[1]), &rec1)
	if rec1["metric"] != "total_ratings" {
		t.Errorf("expected total_ratings, got %v", rec1["metric"])
	}
	if rec1["value"].(float64) != 500 {
		t.Errorf("expected rating value 500, got %v", rec1["value"])
	}

	var rec2 map[string]interface{}
	_ = json.Unmarshal([]byte(lines[2]), &rec2)
	if rec2["metric"] != "total_reviews" {
		t.Errorf("expected total_reviews, got %v", rec2["metric"])
	}
	if rec2["value"].(float64) != 2 {
		t.Errorf("expected reviews value 2, got %v", rec2["value"])
	}

	var rec3 map[string]interface{}
	_ = json.Unmarshal([]byte(lines[3]), &rec3)
	if rec3["metric"] != "total_ratings" {
		t.Errorf("expected total_ratings, got %v", rec3["metric"])
	}
	if rec3["value"].(float64) != 450 {
		t.Errorf("expected scaled rating value 450 unchanged, got %v", rec3["value"])
	}
}

func TestValidateData_NoProjectFilter(t *testing.T) {
	dataDir := testDataDir(t)

	writeTestRecords(t, metricsPath(dataDir, "pypi", "anything", "2025-06-01"),
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "anything", Target: "x", Date: "2025-06-01", Value: 100},
	)

	result, err := ValidateData(dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Issues) != 0 {
		t.Errorf("expected 0 issues when no project filter, got %d", len(result.Issues))
	}
}

func TestDateMatchesFile(t *testing.T) {
	tests := []struct {
		date      string
		fileRange string
		want      bool
	}{
		{"2025-06-01", "2025-06-01", true},
		{"2025-06-02", "2025-06-01", false},
		{"2025-06-15", "2025-06", true},
		{"2025-07-01", "2025-06", false},
		{"2025-03-15", "2025", true},
		{"2026-01-01", "2025", false},
	}

	for _, tt := range tests {
		got := dateMatchesFile(tt.date, tt.fileRange)
		if got != tt.want {
			t.Errorf("dateMatchesFile(%q, %q) = %v, want %v", tt.date, tt.fileRange, got, tt.want)
		}
	}
}
