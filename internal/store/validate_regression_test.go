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
