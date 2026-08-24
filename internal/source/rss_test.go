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
    <vr:software>software/pak</vr:software>
    <vr:software>software/renv</vr:software>
    <vr:language>R</vr:language>
    <vr:language>Python</vr:language>
    <vr:people>people/tomasz-kalinowski</vr:people>
    <vr:people>people/charlie-gao</vr:people>
    <vr:topic>Best Practices</vr:topic>
    <vr:topic>Publishing</vr:topic>
    <vr:source>tidyverse</vr:source>
    <vr:image>blog/2026-07-23_ir-0-1-0/terrarium.png</vr:image>
    <content type="text/markdown"><![CDATA[## Introduction

ir lets you run portable R scripts.]]></content>
  </entry>
  <entry>
    <id>https://opensource.posit.co/blog/2026-07-10_positron/</id>
    <title>Positron 2026.07</title>
    <link rel="alternate" href="https://opensource.posit.co/blog/2026-07-10_positron/"/>
    <published>2026-07-10T00:00:00Z</published>
    <updated>2026-07-10T12:00:00Z</updated>
    <summary>What is new in Positron.</summary>
    <author>
      <name>Davis Vaughan</name>
      <uri>https://opensource.posit.co/people/davis-vaughan/</uri>
    </author>
    <vr:type>post</vr:type>
    <vr:software>software/positron</vr:software>
    <vr:language>R</vr:language>
    <vr:people>people/davis-vaughan</vr:people>
    <vr:topic>IDE</vr:topic>
    <vr:source>positron</vr:source>
    <vr:image>blog/2026-07-10_positron/hero.png</vr:image>
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
	if records[0].Metric != "total_items" || records[0].Value != 2 {
		t.Errorf("unexpected metric record: %+v", records[0])
	}
	if records[0].Target != srv.URL || records[0].Date != "2026-07-24" {
		t.Errorf("unexpected metric target/date: %+v", records[0])
	}

	entries := r.ContentEntries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 content entries, got %d", len(entries))
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
	authors, ok := e.Extra["authors"].([]any)
	if !ok || len(authors) != 2 {
		t.Fatalf("authors metadata = %#v", e.Extra["authors"])
	}
	first := authors[0].(map[string]any)
	if first["name"] != "Tomasz Kalinowski" || first["uri"] != "https://opensource.posit.co/people/tomasz-kalinowski/" {
		t.Errorf("author[0] = %#v", first)
	}

	// Repeated chardata vr:software → string array.
	software, ok := e.Extra["software"].([]any)
	if !ok || len(software) != 2 {
		t.Fatalf("software metadata = %#v", e.Extra["software"])
	}
	if software[0] != "software/pak" || software[1] != "software/renv" {
		t.Errorf("software = %#v", software)
	}

	// vr:language — repeats across the feed, so always array.
	lang, ok := e.Extra["language"].([]any)
	if !ok || len(lang) != 2 {
		t.Fatalf("language metadata = %#v (want 2-element array)", e.Extra["language"])
	}
	if lang[0] != "R" || lang[1] != "Python" {
		t.Errorf("language = %#v", lang)
	}

	// vr:people — plural string array.
	people, ok := e.Extra["people"].([]any)
	if !ok || len(people) != 2 {
		t.Fatalf("people metadata = %#v (want 2-element string array)", e.Extra["people"])
	}
	if people[0] != "people/tomasz-kalinowski" || people[1] != "people/charlie-gao" {
		t.Errorf("people = %#v", people)
	}

	// vr:topic — plural string array.
	topics, ok := e.Extra["topic"].([]any)
	if !ok || len(topics) != 2 {
		t.Fatalf("topic metadata = %#v (want 2-element array)", e.Extra["topic"])
	}
	if topics[0] != "Best Practices" || topics[1] != "Publishing" {
		t.Errorf("topic = %#v", topics)
	}

	// Chardata-only vr:source → scalar string (not a plural relation).
	if src, ok := e.Extra["source"].(string); !ok || src != "tidyverse" {
		t.Errorf("source metadata = %#v", e.Extra["source"])
	}

	// vr:image — chardata scalar string (not a plural relation).
	if img, ok := e.Extra["image"].(string); !ok || img != "blog/2026-07-23_ir-0-1-0/terrarium.png" {
		t.Errorf("image metadata = %#v (want scalar string)", e.Extra["image"])
	}

	// vr:type must NOT appear in metadata.
	if _, present := e.Extra["type"]; present {
		t.Errorf("vr:type should be promoted, not in metadata: %#v", e.Extra["type"])
	}

	// Second entry has single occurrences of each plural field — they must
	// still be arrays because the feed-level pre-scan saw repetitions in entry 1.
	e2 := entries[1]
	if sw, ok := e2.Extra["software"].([]any); !ok || len(sw) != 1 || sw[0] != "software/positron" {
		t.Errorf("entry2 software = %#v (want single-element array)", e2.Extra["software"])
	}
	if lang, ok := e2.Extra["language"].([]any); !ok || len(lang) != 1 || lang[0] != "R" {
		t.Errorf("entry2 language = %#v (want single-element array)", e2.Extra["language"])
	}
	if ppl, ok := e2.Extra["people"].([]any); !ok || len(ppl) != 1 || ppl[0] != "people/davis-vaughan" {
		t.Errorf("entry2 people = %#v (want single-element array)", e2.Extra["people"])
	}
	if topics, ok := e2.Extra["topic"].([]any); !ok || len(topics) != 1 || topics[0] != "IDE" {
		t.Errorf("entry2 topic = %#v (want single-element array)", e2.Extra["topic"])
	}
	// vr:source and vr:image never repeat in any entry → scalar.
	if _, ok := e2.Extra["source"].(string); !ok {
		t.Errorf("entry2 source = %#v (want scalar string)", e2.Extra["source"])
	}
	if _, ok := e2.Extra["image"].(string); !ok {
		t.Errorf("entry2 image = %#v (want scalar string)", e2.Extra["image"])
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
	if e.Content != "Show notes..." {
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
	authors, ok := e.Extra["authors"].([]any)
	if !ok || len(authors) != 1 {
		t.Fatalf("authors = %#v", e.Extra["authors"])
	}
	if authors[0].(map[string]any)["name"] != "Jane Doe" {
		t.Errorf("author = %#v", authors[0])
	}

	// itunes:* extensions passthrough as scalars under bare local names.
	if e.Extra["duration"] != "3600" {
		t.Errorf("duration = %#v", e.Extra["duration"])
	}
	if e.Extra["episode"] != "42" {
		t.Errorf("episode = %#v", e.Extra["episode"])
	}
}

// TestRSSPluralExtensionCardinality guards feed-level pre-scan cardinality:
// if any entry has 2+ occurrences of an element name, that name is an array in
// ALL entries — even those with only one occurrence.
func TestRSSPluralExtensionCardinality(t *testing.T) {
	const feed = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:vr="https://opensource.posit.co/ns/content">
  <entry>
    <id>https://example.com/one</id>
    <title>One</title>
    <updated>2026-01-01T00:00:00Z</updated>
    <vr:type>post</vr:type>
    <vr:software>software/ggsql</vr:software>
    <vr:software>software/dplyr</vr:software>
    <vr:language>R</vr:language>
    <vr:language>Python</vr:language>
    <vr:people>people/jeroen-janssens</vr:people>
    <vr:people>people/hadley-wickham</vr:people>
    <vr:topic>Data Science</vr:topic>
    <vr:topic>Visualization</vr:topic>
    <vr:image>blog/one/logo.png</vr:image>
  </entry>
  <entry>
    <id>https://example.com/two</id>
    <title>Two</title>
    <updated>2026-01-02T00:00:00Z</updated>
    <vr:type>post</vr:type>
    <vr:software>software/positron</vr:software>
    <vr:language>R</vr:language>
    <vr:people>people/davis-vaughan</vr:people>
    <vr:topic>IDE</vr:topic>
    <vr:image>blog/two/logo.png</vr:image>
  </entry>
</feed>`

	srv := serveFeed(t, feed)
	r, _ := fetchOne(t, srv.URL)
	entries := r.ContentEntries()

	// Entry 2 has one of each plural element — still must be arrays because
	// the feed-level scan saw repetitions in entry 1.
	e := entries[1]
	for _, tc := range []struct{ key, want string }{
		{"software", "software/positron"},
		{"language", "R"},
		{"people", "people/davis-vaughan"},
		{"topic", "IDE"},
	} {
		arr, ok := e.Extra[tc.key].([]any)
		if !ok || len(arr) != 1 {
			t.Fatalf("%s: want single-element array, got %T: %#v", tc.key, e.Extra[tc.key], e.Extra[tc.key])
		}
		if arr[0] != tc.want {
			t.Errorf("%s[0] = %#v, want %q", tc.key, arr[0], tc.want)
		}
	}

	// Non-plural element stays scalar (image never repeats in any entry).
	if img, ok := e.Extra["image"].(string); !ok || img != "blog/two/logo.png" {
		t.Fatalf("image: want scalar string, got %T: %#v", e.Extra["image"], e.Extra["image"])
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
	// media:content is not a plural relation → single object under "content";
	// the media:title scalar lands under "title".
	if _, ok := e.Extra["content"].(map[string]any); !ok {
		t.Errorf("media:content should be captured in metadata as an object, got %#v", e.Extra["content"])
	}
	if e.Extra["title"] != "Media Title" {
		t.Errorf("media:title metadata = %#v", e.Extra["title"])
	}
}
