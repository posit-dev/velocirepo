package store

// Regression test for the validate-data finding in
// docs/review/2026-10-correctness-review.md. Fails at deabcc6.

import (
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
