package store

import (
	"path/filepath"
	"testing"

	"github.com/posit-dev/velocirepo/internal/source"
)

func TestWriteContent(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	dur1 := int64(330)
	dur2 := int64(600)
	entries := []source.ContentEntry{
		{Source: "youtube", Target: "@Test", ID: "vid1", Title: "First Video", PublishedAt: "2025-01-01T10:00:00Z", Duration: &dur1, Type: "video"},
		{Source: "youtube", Target: "@Test", ID: "vid2", Title: "Second Video", PublishedAt: "2025-02-01T10:00:00Z", Duration: &dur2, Type: "video"},
	}

	if err := WriteContent(dataDir, "youtube", "my-proj", "videos.jsonl", entries); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dataDir, "content", "youtube", "my-proj", "videos.jsonl")
	read, err := ReadContent(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(read) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(read))
	}
	if read[0].ID != "vid1" || read[0].Title != "First Video" {
		t.Errorf("unexpected entry 0: %+v", read[0])
	}
	// WriteContent stamps project_id from the write path on every line.
	if read[0].ProjectID != "my-proj" || read[1].ProjectID != "my-proj" {
		t.Errorf("expected project_id stamped as my-proj, got %q and %q", read[0].ProjectID, read[1].ProjectID)
	}
}

func TestWriteContentMerge(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	dur := int64(60)
	initial := []source.ContentEntry{
		{Source: "youtube", Target: "@Test", ID: "vid1", Title: "Old Title", PublishedAt: "2025-01-01T10:00:00Z", Duration: &dur, Type: "video"},
		{Source: "youtube", Target: "@Test", ID: "vid2", Title: "Video Two", PublishedAt: "2025-02-01T10:00:00Z", Duration: &dur, Type: "video"},
	}
	if err := WriteContent(dataDir, "youtube", "proj", "videos.jsonl", initial); err != nil {
		t.Fatal(err)
	}

	update := []source.ContentEntry{
		{Source: "youtube", Target: "@Test", ID: "vid1", Title: "New Title", PublishedAt: "2025-01-01T10:00:00Z", Duration: &dur, Type: "video"},
		{Source: "youtube", Target: "@Test", ID: "vid3", Title: "Video Three", PublishedAt: "2025-03-01T10:00:00Z", Duration: &dur, Type: "video"},
	}
	if err := WriteContent(dataDir, "youtube", "proj", "videos.jsonl", update); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dataDir, "content", "youtube", "proj", "videos.jsonl")
	read, err := ReadContent(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(read) != 3 {
		t.Fatalf("expected 3 entries after merge, got %d", len(read))
	}
	if read[0].ID != "vid1" || read[0].Title != "New Title" {
		t.Errorf("expected vid1 with updated title, got %+v", read[0])
	}
	if read[1].ID != "vid2" {
		t.Errorf("expected vid2 preserved, got %+v", read[1])
	}
	if read[2].ID != "vid3" {
		t.Errorf("expected vid3 appended, got %+v", read[2])
	}
}

func TestWriteContentRSSFields(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	entries := []source.ContentEntry{
		{
			Source:      "rss",
			Target:      "https://opensource.posit.co/blog/index.md.xml",
			ID:          "https://opensource.posit.co/blog/post/",
			Title:       "A Post",
			Description: "Summary",
			Content:     "## Body\n\nfull markdown",
			PublishedAt: "2026-07-23T00:00:00Z",
			UpdatedAt:   "2026-07-23T21:03:49Z",
			Type:        "post",
		},
	}

	if err := WriteContent(dataDir, "rss", "osw", "blog.jsonl", entries); err != nil {
		t.Fatal(err)
	}
	// Re-write identical entries; upsert-by-id must keep a single line.
	if err := WriteContent(dataDir, "rss", "osw", "blog.jsonl", entries); err != nil {
		t.Fatal(err)
	}

	// content + updated_at surface through the DuckDB view.
	results, _, err := QueryLive(dataDir, nil, nil,
		"SELECT id, content, CAST(updated_at AS VARCHAR) AS updated_at FROM content WHERE source = 'rss'")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 row after upsert, got %d", len(results))
	}
	if results[0]["content"] != "## Body\n\nfull markdown" {
		t.Errorf("content = %v", results[0]["content"])
	}
	if results[0]["updated_at"] != "2026-07-23 21:03:49" {
		t.Errorf("updated_at = %v", results[0]["updated_at"])
	}
}

func TestContentViewEmptyTimestamp(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	// A person entry has updated_at but no published_at; the empty string must
	// not break the view's timestamp cast.
	entries := []source.ContentEntry{
		{
			Source:    "rss",
			Target:    "https://opensource.posit.co/people/index.md.xml",
			ID:        "https://opensource.posit.co/people/jane/",
			Title:     "Jane",
			UpdatedAt: "2026-05-21T18:03:11Z",
			Type:      "person",
		},
	}
	if err := WriteContent(dataDir, "rss", "osw", "people.jsonl", entries); err != nil {
		t.Fatal(err)
	}

	results, _, err := QueryLive(dataDir, nil, nil,
		"SELECT id, published_at FROM content WHERE type = 'person'")
	if err != nil {
		t.Fatalf("query with empty published_at: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 row, got %d", len(results))
	}
	if results[0]["published_at"] != nil {
		t.Errorf("expected NULL published_at, got %v", results[0]["published_at"])
	}
}

func TestContentDuckDBView(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	dur := int64(330)
	entries := []source.ContentEntry{
		{Source: "youtube", Target: "@TestChan", ID: "abc123", Title: "Test Video", PublishedAt: "2025-06-01T10:00:00Z", Duration: &dur, Tags: []string{"go"}, Type: "video"},
	}
	if err := WriteContent(dataDir, "youtube", "proj", "videos.jsonl", entries); err != nil {
		t.Fatal(err)
	}

	results, _, err := QueryLive(dataDir, nil, nil, "SELECT project, source, target, id, title, type FROM content")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 row, got %d", len(results))
	}
	if results[0]["project"] != "proj" {
		t.Errorf("expected project=proj, got %v", results[0]["project"])
	}
	if results[0]["source"] != "youtube" {
		t.Errorf("expected source=youtube, got %v", results[0]["source"])
	}
	if results[0]["target"] != "@TestChan" {
		t.Errorf("expected target=@TestChan, got %v", results[0]["target"])
	}
	if results[0]["id"] != "abc123" {
		t.Errorf("expected id=abc123, got %v", results[0]["id"])
	}
	if results[0]["type"] != "video" {
		t.Errorf("expected type=video, got %v", results[0]["type"])
	}
}
