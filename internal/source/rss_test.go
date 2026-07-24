package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const atomFixture = `<?xml version="1.0" encoding="utf-8" standalone="yes"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:vr="https://opensource.posit.co/ns/content">
  <title>Blog on Posit Open Source</title>
  <id>https://opensource.posit.co/blog/</id>
  <link rel="alternate" href="https://opensource.posit.co/blog/"/>
  <updated>2026-07-24T14:05:27Z</updated>
  <entry>
    <id>https://opensource.posit.co/blog/2026-07-23_ir-0-1-0/</id>
    <title>ir 0.1.0: self-describing R scripts and Quarto documents</title>
    <link rel="alternate" href="https://opensource.posit.co/blog/2026-07-23_ir-0-1-0/"/>
    <published>2026-07-23T00:00:00Z</published>
    <updated>2026-07-23T21:03:49Z</updated>
    <summary>ir is a new command-line tool.</summary>
    <author>
      <name>Tomasz Kalinowski</name>
      <uri>https://opensource.posit.co/people/tomasz-kalinowski/</uri>
    </author>
    <author>
      <name>Charlie Gao</name>
      <uri>https://opensource.posit.co/people/charlie-gao/</uri>
    </author>
    <category term="Reproducibility"/>
    <category term="CLI"/>
    <vr:type>post</vr:type>
    <vr:software term="pak" href="https://opensource.posit.co/software/pak/"/>
    <vr:software term="renv" href="https://opensource.posit.co/software/renv/"/>
    <vr:language term="R" href="https://opensource.posit.co/languages/r/"/>
    <vr:source>tidyverse</vr:source>
    <vr:image href="https://opensource.posit.co/blog/2026-07-23_ir-0-1-0/terrarium.png" alt="A terrarium."/>
    <content type="text/markdown"><![CDATA[## Introduction

ir lets you run portable R scripts.]]></content>
  </entry>
</feed>`

const rssFixture = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"
  xmlns:content="http://purl.org/rss/1.0/modules/content/"
  xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"
  xmlns:dc="http://purl.org/dc/elements/1.1/">
  <channel>
    <title>Example Podcast</title>
    <item>
      <title>Episode 42</title>
      <link>https://example.com/ep/42</link>
      <guid>https://example.com/ep/42</guid>
      <description>Short summary.</description>
      <pubDate>Wed, 01 May 2026 08:00:00 +0000</pubDate>
      <category>Tech</category>
      <category>Interviews</category>
      <content:encoded><![CDATA[<p>Show notes...</p>]]></content:encoded>
      <dc:creator>Jane Doe</dc:creator>
      <dc:date>2026-05-02T09:00:00Z</dc:date>
      <itunes:duration>3600</itunes:duration>
      <itunes:episode>42</itunes:episode>
    </item>
  </channel>
</rss>`

func serveFeed(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fetchOne(t *testing.T, feedURL string) (*RSS, []Record) {
	t.Helper()
	r := &RSS{Client: http.DefaultClient, FeedURL: feedURL}
	records, err := r.Fetch(context.Background(), FetchOptions{
		ProjectID: "open-source-website",
		EndDate:   time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	return r, records
}

func TestRSSAtomMapping(t *testing.T) {
	srv := serveFeed(t, atomFixture)
	r, records := fetchOne(t, srv.URL)

	if len(records) != 1 {
		t.Fatalf("expected 1 metric record, got %d", len(records))
	}
	if records[0].Metric != "total_items" || records[0].Value != 1 {
		t.Errorf("unexpected metric record: %+v", records[0])
	}
	if records[0].Target != srv.URL || records[0].Date != "2026-07-24" {
		t.Errorf("unexpected metric target/date: %+v", records[0])
	}

	entries := r.ContentEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 content entry, got %d", len(entries))
	}
	e := entries[0]

	if e.Source != "rss" {
		t.Errorf("source = %q", e.Source)
	}
	if e.ID != "https://opensource.posit.co/blog/2026-07-23_ir-0-1-0/" {
		t.Errorf("id = %q", e.ID)
	}
	if e.Title != "ir 0.1.0: self-describing R scripts and Quarto documents" {
		t.Errorf("title = %q", e.Title)
	}
	if e.Description != "ir is a new command-line tool." {
		t.Errorf("description = %q", e.Description)
	}
	if e.Content == "" || !strings.Contains(e.Content, "## Introduction") {
		t.Errorf("content body not captured: %q", e.Content)
	}
	if e.PublishedAt != "2026-07-23T00:00:00Z" {
		t.Errorf("published_at = %q", e.PublishedAt)
	}
	if e.UpdatedAt != "2026-07-23T21:03:49Z" {
		t.Errorf("updated_at = %q", e.UpdatedAt)
	}
	if e.URL != "https://opensource.posit.co/blog/2026-07-23_ir-0-1-0/" {
		t.Errorf("url = %q", e.URL)
	}
	if e.Type != "post" {
		t.Errorf("type = %q (vr:type should be promoted)", e.Type)
	}
	if len(e.Tags) != 2 || e.Tags[0] != "Reproducibility" || e.Tags[1] != "CLI" {
		t.Errorf("tags = %v", e.Tags)
	}

	// Authors promoted from <author>.
	authors, ok := e.Metadata["authors"].([]any)
	if !ok || len(authors) != 2 {
		t.Fatalf("authors metadata = %#v", e.Metadata["authors"])
	}
	first := authors[0].(map[string]any)
	if first["name"] != "Tomasz Kalinowski" || first["uri"] != "https://opensource.posit.co/people/tomasz-kalinowski/" {
		t.Errorf("author[0] = %#v", first)
	}

	// Repeated attr-only vr:software → array of objects.
	software, ok := e.Metadata["software"].([]any)
	if !ok || len(software) != 2 {
		t.Fatalf("software metadata = %#v", e.Metadata["software"])
	}
	sw0 := software[0].(map[string]any)
	if sw0["term"] != "pak" || sw0["href"] != "https://opensource.posit.co/software/pak/" {
		t.Errorf("software[0] = %#v", sw0)
	}

	// A single object-valued vr:language is still an array, so the type stays
	// consistent whether an entry has one or many.
	lang, ok := e.Metadata["language"].([]any)
	if !ok || len(lang) != 1 {
		t.Fatalf("language metadata = %#v (want single-element array)", e.Metadata["language"])
	}
	if lang[0].(map[string]any)["term"] != "R" {
		t.Errorf("language[0] = %#v", lang[0])
	}

	// Chardata-only vr:source → string.
	if src, ok := e.Metadata["source"].(string); !ok || src != "tidyverse" {
		t.Errorf("source metadata = %#v", e.Metadata["source"])
	}

	// Single object-valued vr:image → single-element array.
	imgArr, ok := e.Metadata["image"].([]any)
	if !ok || len(imgArr) != 1 {
		t.Fatalf("image metadata = %#v (want single-element array)", e.Metadata["image"])
	}
	if imgArr[0].(map[string]any)["alt"] != "A terrarium." {
		t.Errorf("image[0] = %#v", imgArr[0])
	}

	// vr:type must NOT appear in metadata.
	if _, present := e.Metadata["type"]; present {
		t.Errorf("vr:type should be promoted, not in metadata: %#v", e.Metadata["type"])
	}
}

func TestRSS20Mapping(t *testing.T) {
	srv := serveFeed(t, rssFixture)
	r, records := fetchOne(t, srv.URL)

	if len(records) != 1 || records[0].Value != 1 {
		t.Fatalf("unexpected records: %+v", records)
	}

	entries := r.ContentEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]

	if e.ID != "https://example.com/ep/42" {
		t.Errorf("id = %q", e.ID)
	}
	if e.Title != "Episode 42" {
		t.Errorf("title = %q", e.Title)
	}
	if e.Description != "Short summary." {
		t.Errorf("description = %q", e.Description)
	}
	if e.Content != "<p>Show notes...</p>" {
		t.Errorf("content = %q (content:encoded)", e.Content)
	}
	// pubDate RFC1123Z normalized to RFC3339 UTC.
	if e.PublishedAt != "2026-05-01T08:00:00Z" {
		t.Errorf("published_at = %q", e.PublishedAt)
	}
	// dc:date → updated_at.
	if e.UpdatedAt != "2026-05-02T09:00:00Z" {
		t.Errorf("updated_at = %q", e.UpdatedAt)
	}
	if len(e.Tags) != 2 || e.Tags[0] != "Tech" {
		t.Errorf("tags = %v", e.Tags)
	}

	// dc:creator promoted to authors.
	authors, ok := e.Metadata["authors"].([]any)
	if !ok || len(authors) != 1 {
		t.Fatalf("authors = %#v", e.Metadata["authors"])
	}
	if authors[0].(map[string]any)["name"] != "Jane Doe" {
		t.Errorf("author = %#v", authors[0])
	}

	// itunes:* extensions passthrough as scalars under bare local names.
	if e.Metadata["duration"] != "3600" {
		t.Errorf("duration = %#v", e.Metadata["duration"])
	}
	if e.Metadata["episode"] != "42" {
		t.Errorf("episode = %#v", e.Metadata["episode"])
	}
}

// TestRSSObjectExtensionAlwaysArray guards the invariant that object-valued
// extension elements keep the same JSON type regardless of how many times they
// appear in an entry: an entry with a single <vr:software> must produce an
// array, matching entries that carry several.
func TestRSSObjectExtensionAlwaysArray(t *testing.T) {
	const single = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:vr="https://opensource.posit.co/ns/content">
  <entry>
    <id>https://example.com/one</id>
    <title>One</title>
    <updated>2026-01-01T00:00:00Z</updated>
    <vr:type>post</vr:type>
    <vr:software term="ggsql" href="https://opensource.posit.co/software/ggsql/"/>
  </entry>
</feed>`

	srv := serveFeed(t, single)
	r, _ := fetchOne(t, srv.URL)
	e := r.ContentEntries()[0]

	software, ok := e.Metadata["software"].([]any)
	if !ok {
		t.Fatalf("single vr:software should be an array, got %T: %#v", e.Metadata["software"], e.Metadata["software"])
	}
	if len(software) != 1 || software[0].(map[string]any)["term"] != "ggsql" {
		t.Errorf("software = %#v", software)
	}
}

func TestRSSFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := &RSS{Client: http.DefaultClient, FeedURL: srv.URL}
	if _, err := r.Fetch(context.Background(), FetchOptions{}); err == nil {
		t.Fatal("expected error on 500 response")
	}
}

func TestRSSMalformedFeed(t *testing.T) {
	srv := serveFeed(t, "<html><body>not a feed</body></html>")
	r := &RSS{Client: http.DefaultClient, FeedURL: srv.URL}
	if _, err := r.Fetch(context.Background(), FetchOptions{}); err == nil {
		t.Fatal("expected error on unrecognized root element")
	}
}

func TestFeedFilename(t *testing.T) {
	cases := map[string]string{
		"https://opensource.posit.co/blog/index.md.xml":      "blog.jsonl",
		"https://opensource.posit.co/people/index.md.xml":    "people.jsonl",
		"https://opensource.posit.co/resources/index.md.xml": "resources.jsonl",
		"https://example.com/feed.xml":                       "example-com.jsonl",
		"https://example.com/podcast.xml":                    "podcast.jsonl",
		"https://example.com/":                               "example-com.jsonl",
	}
	for in, want := range cases {
		if got := feedFilename(in); got != want {
			t.Errorf("feedFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRSSContentFilename(t *testing.T) {
	r := &RSS{FeedURL: "https://opensource.posit.co/blog/index.md.xml"}
	if got := r.ContentFilename(); got != "blog.jsonl" {
		t.Errorf("ContentFilename() = %q, want blog.jsonl", got)
	}
}

// TestAtomExtensionSameLocalName guards that extension elements sharing a local
// name with an Atom core element (e.g. <media:content>, <media:title>) flow
// into metadata and do not overwrite the real Atom <content> body or title.
func TestAtomExtensionSameLocalName(t *testing.T) {
	const feed = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:media="http://search.yahoo.com/mrss/">
  <entry>
    <id>https://example.com/1</id>
    <title>Real Title</title>
    <updated>2026-01-01T00:00:00Z</updated>
    <content type="text/markdown">Real body.</content>
    <media:content url="https://example.com/video.mp4" type="video/mp4"/>
    <media:title>Media Title</media:title>
  </entry>
</feed>`

	srv := serveFeed(t, feed)
	r, _ := fetchOne(t, srv.URL)
	e := r.ContentEntries()[0]

	if e.Title != "Real Title" {
		t.Errorf("title overwritten by media:title: %q", e.Title)
	}
	if e.Content != "Real body." {
		t.Errorf("content overwritten by media:content: %q", e.Content)
	}
	// media:content is object-valued → array under "content"; the media:title
	// scalar lands under "title".
	if _, ok := e.Metadata["content"].([]any); !ok {
		t.Errorf("media:content should be captured in metadata, got %#v", e.Metadata["content"])
	}
	if e.Metadata["title"] != "Media Title" {
		t.Errorf("media:title metadata = %#v", e.Metadata["title"])
	}
}
