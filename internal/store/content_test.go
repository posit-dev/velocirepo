package store

import (
	"path/filepath"
	"strings"
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

func TestWriteContentDistinctTargetsSameID(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	// Two different feeds slug to the same file (feed.jsonl) and happen to share
	// an item id. Merging by (target, id) must keep both as distinct rows rather
	// than letting one overwrite the other.
	first := []source.ContentEntry{
		{Source: "rss", Target: "https://a.example.com/feed.xml", ID: "1", Title: "From A"},
	}
	second := []source.ContentEntry{
		{Source: "rss", Target: "https://b.example.com/feed.xml", ID: "1", Title: "From B"},
	}
	if err := WriteContent(dataDir, "rss", "proj", "feed.jsonl", first); err != nil {
		t.Fatal(err)
	}
	if err := WriteContent(dataDir, "rss", "proj", "feed.jsonl", second); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dataDir, "content", "rss", "proj", "feed.jsonl")
	read, err := ReadContent(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(read) != 2 {
		t.Fatalf("expected 2 distinct rows for distinct targets, got %d: %+v", len(read), read)
	}

	// Re-writing the first feed's entry updates it in place, leaving 2 rows.
	updated := []source.ContentEntry{
		{Source: "rss", Target: "https://a.example.com/feed.xml", ID: "1", Title: "From A v2"},
	}
	if err := WriteContent(dataDir, "rss", "proj", "feed.jsonl", updated); err != nil {
		t.Fatal(err)
	}
	read, err = ReadContent(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(read) != 2 {
		t.Fatalf("expected 2 rows after in-place update, got %d", len(read))
	}
	for _, e := range read {
		if e.Target == "https://a.example.com/feed.xml" && e.Title != "From A v2" {
			t.Errorf("feed A entry not updated: %+v", e)
		}
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

func TestWriteContentLargeLine(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	largeBody := strings.Repeat("x", 2*1024*1024) // 2MB content field
	initial := []source.ContentEntry{
		{Source: "rss", Target: "https://example.com/feed.xml", ID: "big", Title: "Big Post", Content: largeBody, PublishedAt: "2026-01-01T00:00:00Z"},
	}
	if err := WriteContent(dataDir, "rss", "proj", "blog.jsonl", initial); err != nil {
		t.Fatal(err)
	}

	// A subsequent write must be able to read back the large line and merge.
	update := []source.ContentEntry{
		{Source: "rss", Target: "https://example.com/feed.xml", ID: "small", Title: "Small Post", PublishedAt: "2026-02-01T00:00:00Z"},
	}
	if err := WriteContent(dataDir, "rss", "proj", "blog.jsonl", update); err != nil {
		t.Fatalf("WriteContent failed after large line: %v", err)
	}

	path := filepath.Join(dataDir, "content", "rss", "proj", "blog.jsonl")
	read, err := ReadContent(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(read) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(read))
	}
	if read[0].ID != "big" || len(read[0].Content) != 2*1024*1024 {
		t.Errorf("large entry not preserved: id=%s content_len=%d", read[0].ID, len(read[0].Content))
	}
	if read[1].ID != "small" {
		t.Errorf("expected small entry appended, got id=%s", read[1].ID)
	}
}
