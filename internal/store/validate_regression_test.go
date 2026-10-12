package store

// Regression test for the validate-data finding in
// docs/review/2026-10-correctness-review.md. Fails at deabcc6.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/posit-dev/velocirepo/internal/source"
)

// `validate-data --fix` decides whether a file holds events by looking for
// "data/events/" in its path. With a data directory that is not named "data"
// (data.dir is configurable), event files are deduplicated as metric records,
// which drops every event field the record type doesn't have.
func TestFixDuplicatesKeepsEventsOutsideDefaultDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "metrics-store")
	path := eventsPath(dataDir, "github", "proj", "2025-06-01")
	ref := 42
	star := source.Event{Source: "github", Type: "star", ProjectID: "proj", Target: "org/repo", Datetime: "2025-06-01T10:00:00Z", User: "alice"}
	issue := source.Event{Source: "github", Type: "issue_open", ProjectID: "proj", Target: "org/repo", Datetime: "2025-06-01T11:00:00Z", Ref: &ref, User: "bob"}
	writeTestEvents(t, path, star, star, issue)

	result := FixDuplicates([]string{path})
	if len(result.Errors) > 0 {
		t.Fatalf("FixDuplicates errors: %v", result.Errors)
	}

	got, err := ReadEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events after removing one duplicate, want 2: %+v", len(got), got)
	}
	for _, e := range got {
		if e.Type == "" || e.Datetime == "" || e.User == "" {
			t.Fatalf("event lost fields after FixDuplicates: %+v", e)
		}
	}
}

// Content entries from different targets may share an ID (two feeds in one
// file): that is not a duplicate. A real duplicate is removed as content, not
// rewritten as a metric record.
func TestFixDuplicatesKeepsContentEntries(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	entries := []source.ContentEntry{
		{Source: "rss", Target: "https://a.example/feed", ID: "1", Title: "First post", PublishedAt: "2025-06-01T00:00:00Z"},
		{Source: "rss", Target: "https://b.example/feed", ID: "1", Title: "Other feed", PublishedAt: "2025-06-01T00:00:00Z"},
		{Source: "rss", Target: "https://a.example/feed", ID: "2", Title: "Second post", PublishedAt: "2025-06-02T00:00:00Z"},
	}
	if err := WriteContent(dataDir, "rss", "proj", "blog.jsonl", entries); err != nil {
		t.Fatal(err)
	}
	path := contentPath(dataDir, "rss", "proj", "blog")
	// A true duplicate: the same entry appended again with a corrected title.
	lines := readRawLines(t, path)
	lines = append(lines, `{"source":"rss","project_id":"proj","target":"https://a.example/feed","id":"1","title":"First post (edited)","published_at":"2025-06-01T00:00:00Z"}`)
	writeTestRaw(t, path, lines)

	result, err := ValidateData(dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var duplicates []string
	for _, issue := range result.Issues {
		if issue.Type == IssueDuplicate {
			duplicates = append(duplicates, issue.Message)
		}
	}
	if len(duplicates) != 1 {
		t.Fatalf("duplicates = %v, want only the repeated a.example entry", duplicates)
	}

	if fix := FixDuplicates([]string{path}); len(fix.Errors) > 0 || fix.Fixed != 1 {
		t.Fatalf("FixDuplicates = %+v, want 1 fixed", fix)
	}
	got, err := ReadContent(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(got), got)
	}
	want := []string{"First post (edited)", "Other feed", "Second post"}
	for i, e := range got {
		if e.Title != want[i] || e.ID == "" || e.Target == "" {
			t.Errorf("entry %d = %+v, want title %q with id and target intact", i, e, want[i])
		}
	}
}

func TestFixDateMismatchesRegroupsEventsOutsideDefaultDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "metrics-store")
	path := eventsPath(dataDir, "github", "proj", "2025-06-01")
	writeTestEvents(t, path,
		source.Event{Source: "github", Type: "star", ProjectID: "proj", Target: "org/repo", Datetime: "2025-06-01T10:00:00Z", User: "alice"},
		source.Event{Source: "github", Type: "star", ProjectID: "proj", Target: "org/repo", Datetime: "2025-06-02T10:00:00Z", User: "bob"},
	)

	if fix := FixDateMismatches([]string{path}); len(fix.Errors) > 0 || fix.Fixed != 1 {
		t.Fatalf("FixDateMismatches = %+v, want 1 moved", fix)
	}
	moved, err := ReadEvents(eventsPath(dataDir, "github", "proj", "2025-06-02"))
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 || moved[0].User != "bob" || moved[0].Type != "star" {
		t.Errorf("2025-06-02 = %+v, want bob's star", moved)
	}
}

func TestValidateReportsAndMergesPeriodOverlaps(t *testing.T) {
	dataDir := t.TempDir()
	rec := func(date string, value int64) source.Record {
		return source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "proj", Target: "pkg", Date: date, Value: value}
	}
	writeTestRecords(t, metricsPath(dataDir, "pypi", "proj", "2024"), rec("2024-11-30", 1), rec("2024-12-15", 2))
	writeTestRecords(t, metricsPath(dataDir, "pypi", "proj", "2024-12"), rec("2024-12-15", 20), rec("2024-12-16", 21))
	writeTestRecords(t, metricsPath(dataDir, "pypi", "proj", "2025-01"), rec("2025-01-14", 3), rec("2025-01-15", 4))
	writeTestRecords(t, metricsPath(dataDir, "pypi", "proj", "2025-01-15"), rec("2025-01-15", 40))
	star := source.Event{Source: "github", Type: "star", ProjectID: "proj", Target: "org/repo", User: "alice"}
	first, late := star, star
	first.Datetime, late.Datetime, late.User = "2025-01-10T10:00:00Z", "2025-01-20T10:00:00Z", "bob"
	writeTestEvents(t, eventsPath(dataDir, "github", "proj", "2025-01"), first)
	writeTestEvents(t, eventsPath(dataDir, "github", "proj", "2025-01-20"), late)

	result, err := ValidateData(dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var overlaps []string
	for _, issue := range result.Issues {
		if issue.Type == IssuePeriodOverlap {
			overlaps = append(overlaps, issue.Path)
		}
	}
	if len(overlaps) != 3 {
		t.Fatalf("overlaps = %v, want 2024, 2025-01 (metrics) and 2025-01 (events)", overlaps)
	}

	if fix := FixPeriodOverlaps(overlaps); len(fix.Errors) > 0 || fix.Fixed != 3 {
		t.Fatalf("FixPeriodOverlaps = %+v, want 3 files merged", fix)
	}
	if names := projectFileNames(t, MetricsProjectDir(dataDir, "pypi", "proj")); len(names) != 2 || names[0] != "2024.jsonl" || names[1] != "2025-01.jsonl" {
		t.Fatalf("metrics files = %v, want 2024.jsonl and 2025-01.jsonl", names)
	}
	values := map[string]int64{}
	for _, r := range readProjectRecords(t, dataDir, "pypi", "proj") {
		values[r.Date] = r.Value
	}
	want := map[string]int64{"2024-11-30": 1, "2024-12-15": 20, "2024-12-16": 21, "2025-01-14": 3, "2025-01-15": 40}
	for date, v := range want {
		if values[date] != v {
			t.Errorf("%s = %d, want %d (newer files win)", date, values[date], v)
		}
	}
	if got := len(readProjectEvents(t, dataDir, "github", "proj")); got != 2 {
		t.Errorf("got %d events after merging, want 2", got)
	}
}

func TestValidateReportsLeftoverTempFiles(t *testing.T) {
	dataDir := t.TempDir()
	writeTestRecords(t, metricsPath(dataDir, "pypi", "proj", "2025-01-01"),
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "proj", Date: "2025-01-01", Value: 1})
	temps := []string{
		filepath.Join(MetricsProjectDir(dataDir, "pypi", "proj"), ".tmp-123.jsonl"),
		filepath.Join(MetricsProjectDir(dataDir, "pypi", "proj"), ".2025-01-02.jsonl.456.tmp"),
		filepath.Join(EventsProjectDir(dataDir, "github", "proj"), ".tmp-789.jsonl"),
		filepath.Join(ContentProjectDir(dataDir, "rss", "proj"), ".posts.jsonl.1.tmp"),
	}
	for _, path := range temps {
		writeTestRaw(t, path, []string{`{}`})
	}

	result, err := ValidateData(dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, issue := range result.Issues {
		switch issue.Type {
		case IssueLeftoverTempFile:
			found = append(found, issue.Path)
		case IssueUnexpectedFile:
			t.Errorf("temp file reported as unexpected: %s", issue.Path)
		}
	}
	if len(found) != len(temps) {
		t.Fatalf("leftover temp files = %v, want %d", found, len(temps))
	}

	if fix := FixLeftoverTempFiles(found); len(fix.Errors) > 0 || fix.Fixed != len(temps) {
		t.Fatalf("FixLeftoverTempFiles = %+v", fix)
	}
	for _, path := range temps {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s still exists", path)
		}
	}
}
