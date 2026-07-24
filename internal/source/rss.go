package source

import (
	"context"
	"crypto/sha1"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/posit-dev/velocirepo/internal/dateutil"
)

// RSS reads an RSS 2.0 or Atom feed and maps each item to a ContentEntry. It
// also emits a single total_items metric per fetch, keyed on the feed URL.
type RSS struct {
	Client  *http.Client
	FeedURL string

	contentEntries []ContentEntry
}

func (r *RSS) Name() string { return "rss" }

func (r *RSS) ContentEntries() []ContentEntry {
	return r.contentEntries
}

// ContentFilename derives a stable, human-readable file name from the feed URL
// so multiple feeds under one project don't collide.
func (r *RSS) ContentFilename() string {
	return feedFilename(r.FeedURL)
}

func (r *RSS) Fetch(ctx context.Context, opts FetchOptions) ([]Record, error) {
	r.contentEntries = nil

	body, err := doRequest(ctx, r.Client, httpJSONRequest{
		URL:              r.FeedURL,
		RequestError:     "request",
		StatusError:      "rss feed returned",
		IncludeErrorBody: true,
	})
	if err != nil {
		return nil, err
	}

	items, err := parseFeed(body)
	if err != nil {
		return nil, fmt.Errorf("parse feed %s: %w", r.FeedURL, err)
	}

	for _, item := range items {
		r.contentEntries = append(r.contentEntries, item.toContentEntry(r.FeedURL))
	}

	return []Record{{
		Source:    "rss",
		Metric:    "total_items",
		ProjectID: opts.ProjectID,
		Target:    r.FeedURL,
		Date:      dateutil.FormatDate(opts.EndDate),
		Value:     int64(len(items)),
	}}, nil
}

// feedItem is the format-agnostic internal model. Both RSS and Atom parse into
// this shape before mapping to ContentEntry.
type feedItem struct {
	ID          string
	Title       string
	Description string // short summary
	Content     string // full body
	Published   string // normalized RFC3339
	Updated     string // normalized RFC3339
	URL         string
	Tags        []string
	Type        string
	Metadata    map[string]any
}

func (it feedItem) toContentEntry(feedURL string) ContentEntry {
	var metadata map[string]any
	if len(it.Metadata) > 0 {
		metadata = it.Metadata
	}
	return ContentEntry{
		Source:      "rss",
		Target:      feedURL,
		ID:          it.ID,
		Title:       it.Title,
		Description: it.Description,
		Content:     it.Content,
		PublishedAt: it.Published,
		UpdatedAt:   it.Updated,
		URL:         it.URL,
		Tags:        it.Tags,
		Type:        it.Type,
		Metadata:    metadata,
	}
}

// parseFeed sniffs the root element and parses with the matching struct.
func parseFeed(body []byte) ([]feedItem, error) {
	root, err := sniffRoot(body)
	if err != nil {
		return nil, err
	}

	switch root {
	case "feed":
		var f atomFeed
		if err := xml.Unmarshal(body, &f); err != nil {
			return nil, err
		}
		items := make([]feedItem, 0, len(f.Entries))
		for _, e := range f.Entries {
			items = append(items, e.toFeedItem())
		}
		return items, nil
	case "rss", "rdf":
		var f rssFeed
		if err := xml.Unmarshal(body, &f); err != nil {
			return nil, err
		}
		items := make([]feedItem, 0, len(f.Channel.Items))
		for _, i := range f.Channel.Items {
			items = append(items, i.toFeedItem())
		}
		return items, nil
	default:
		return nil, fmt.Errorf("unrecognized feed root element %q", root)
	}
}

// sniffRoot returns the local name of the first XML element, lowercased.
func sniffRoot(body []byte) (string, error) {
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", fmt.Errorf("read xml: %w", err)
		}
		if start, ok := tok.(xml.StartElement); ok {
			return strings.ToLower(start.Name.Local), nil
		}
	}
}

// --- Atom ---

// Atom core elements are namespace-qualified so that same-local-name extension
// elements from other namespaces (e.g. <media:content>, <media:title>) are not
// swallowed by these fields and instead flow through Extra into metadata.
type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Entries []atomEntry `xml:"http://www.w3.org/2005/Atom entry"`
}

type atomEntry struct {
	ID        string       `xml:"http://www.w3.org/2005/Atom id"`
	Title     string       `xml:"http://www.w3.org/2005/Atom title"`
	Summary   string       `xml:"http://www.w3.org/2005/Atom summary"`
	Content   atomText     `xml:"http://www.w3.org/2005/Atom content"`
	Published string       `xml:"http://www.w3.org/2005/Atom published"`
	Updated   string       `xml:"http://www.w3.org/2005/Atom updated"`
	Links     []atomLink   `xml:"http://www.w3.org/2005/Atom link"`
	Cats      []atomCat    `xml:"http://www.w3.org/2005/Atom category"`
	Authors   []atomAuthor `xml:"http://www.w3.org/2005/Atom author"`
	Extra     []xmlAny     `xml:",any"`
}

type atomText struct {
	Chardata string `xml:",chardata"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

type atomCat struct {
	Term string `xml:"term,attr"`
}

type atomAuthor struct {
	Name string `xml:"http://www.w3.org/2005/Atom name"`
	URI  string `xml:"http://www.w3.org/2005/Atom uri"`
}

func (e atomEntry) toFeedItem() feedItem {
	it := feedItem{
		ID:          strings.TrimSpace(e.ID),
		Title:       strings.TrimSpace(e.Title),
		Description: strings.TrimSpace(e.Summary),
		Content:     strings.TrimSpace(e.Content.Chardata),
		Published:   normalizeTime(e.Published),
		Updated:     normalizeTime(e.Updated),
		URL:         atomAlternate(e.Links),
	}

	for _, c := range e.Cats {
		if c.Term != "" {
			it.Tags = append(it.Tags, c.Term)
		}
	}

	metadata := map[string]any{}

	if authors := atomAuthors(e.Authors); len(authors) > 0 {
		metadata["authors"] = authors
	}

	// vr:type is promoted to the top-level Type field; every other extension
	// element flows through the generic walker into metadata.
	var extras []xmlAny
	for _, x := range e.Extra {
		if x.XMLName.Local == "type" && x.XMLName.Space == vrNamespace {
			it.Type = strings.TrimSpace(x.Chardata)
			continue
		}
		extras = append(extras, x)
	}
	collectExtensions(metadata, extras)

	if len(metadata) > 0 {
		it.Metadata = metadata
	}
	return it
}

func atomAlternate(links []atomLink) string {
	for _, l := range links {
		if l.Rel == "alternate" || l.Rel == "" {
			return l.Href
		}
	}
	if len(links) > 0 {
		return links[0].Href
	}
	return ""
}

func atomAuthors(authors []atomAuthor) []any {
	var out []any
	for _, a := range authors {
		if a.Name == "" && a.URI == "" {
			continue
		}
		author := map[string]any{}
		if a.Name != "" {
			author["name"] = strings.TrimSpace(a.Name)
		}
		if a.URI != "" {
			author["uri"] = strings.TrimSpace(a.URI)
		}
		out = append(out, author)
	}
	return out
}

// --- RSS 2.0 (and RDF/RSS 1.0 item shape) ---

type rssFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Items []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	GUID        string   `xml:"guid"`
	Description string   `xml:"description"`
	PubDate     string   `xml:"pubDate"`
	Cats        []string `xml:"category"`
	Author      string   `xml:"author"`
	Extra       []xmlAny `xml:",any"`
}

func (i rssItem) toFeedItem() feedItem {
	it := feedItem{
		ID:          strings.TrimSpace(firstNonEmpty(i.GUID, i.Link)),
		Title:       strings.TrimSpace(i.Title),
		Description: strings.TrimSpace(i.Description),
		Published:   normalizeTime(i.PubDate),
		URL:         strings.TrimSpace(i.Link),
	}

	for _, c := range i.Cats {
		if t := strings.TrimSpace(c); t != "" {
			it.Tags = append(it.Tags, t)
		}
	}

	metadata := map[string]any{}
	if a := strings.TrimSpace(i.Author); a != "" {
		metadata["authors"] = []any{map[string]any{"name": a}}
	}

	// Pull well-known namespaced elements out of Extra before the generic walk:
	// content:encoded → Content, dc:date/atom:updated → Updated, dc:creator →
	// authors. Everything else flows through collectExtensions.
	var extras []xmlAny
	for _, x := range i.Extra {
		switch {
		case x.XMLName.Local == "encoded" && x.XMLName.Space == contentNamespace:
			it.Content = strings.TrimSpace(x.Chardata)
		case x.XMLName.Local == "updated" && x.XMLName.Space == atomNamespace:
			it.Updated = normalizeTime(x.Chardata)
		case x.XMLName.Local == "date" && x.XMLName.Space == dcNamespace:
			if it.Updated == "" {
				it.Updated = normalizeTime(x.Chardata)
			}
			extras = append(extras, x)
		case x.XMLName.Local == "creator" && x.XMLName.Space == dcNamespace:
			if _, ok := metadata["authors"]; !ok {
				metadata["authors"] = []any{map[string]any{"name": strings.TrimSpace(x.Chardata)}}
			}
		default:
			extras = append(extras, x)
		}
	}
	collectExtensions(metadata, extras)

	if len(metadata) > 0 {
		it.Metadata = metadata
	}
	return it
}

// --- Generic extension-element capture ---

type xmlAny struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Chardata string     `xml:",chardata"`
	Children []xmlAny   `xml:",any"`
}

const (
	vrNamespace      = "https://opensource.posit.co/ns/content"
	itunesNamespace  = "http://www.itunes.com/dtds/podcast-1.0.dtd"
	dcNamespace      = "http://purl.org/dc/elements/1.1/"
	mediaNamespace   = "http://search.yahoo.com/mrss/"
	contentNamespace = "http://purl.org/rss/1.0/modules/content/"
	atomNamespace    = "http://www.w3.org/2005/Atom"
)

var knownPrefixes = map[string]string{
	vrNamespace:      "vr",
	itunesNamespace:  "itunes",
	dcNamespace:      "dc",
	mediaNamespace:   "media",
	contentNamespace: "content",
	atomNamespace:    "atom",
}

// collectExtensions walks extension elements and folds them into metadata. The
// key is the bare local name, falling back to a namespace-prefixed key only
// when two namespaces collide on the same local name within this entry.
//
// To keep metadata types consistent across entries (a feed may carry one
// <vr:software> in one post and several in another), the JSON type is chosen
// from the element's *structure*, not its count:
//
//   - Object-valued elements (attributes and/or children, e.g. vr:software,
//     vr:topic, vr:image) are collection/record-like and are ALWAYS arrays,
//     even when a single occurrence appears.
//   - Plain chardata scalars (e.g. vr:source, itunes:duration) stay scalars,
//     and only become an array if the same element repeats within the entry.
func collectExtensions(metadata map[string]any, extras []xmlAny) {
	if len(extras) == 0 {
		return
	}

	// Detect local-name collisions across differing namespaces.
	spacesByLocal := map[string]map[string]bool{}
	for _, x := range extras {
		if _, ok := spacesByLocal[x.XMLName.Local]; !ok {
			spacesByLocal[x.XMLName.Local] = map[string]bool{}
		}
		spacesByLocal[x.XMLName.Local][x.XMLName.Space] = true
	}

	for _, x := range extras {
		key := x.XMLName.Local
		if len(spacesByLocal[key]) > 1 {
			key = prefixedKey(x.XMLName)
		}
		val := xmlAnyValue(x)
		_, isObject := val.(map[string]any)
		appendMetadata(metadata, key, val, isObject)
	}
}

// appendMetadata folds val into metadata[key]. When forceArray is set (object
// values), the key is always an array; otherwise scalars are stored bare and
// only promoted to an array on repetition.
func appendMetadata(metadata map[string]any, key string, val any, forceArray bool) {
	existing, ok := metadata[key]
	if !ok {
		if forceArray {
			metadata[key] = []any{val}
		} else {
			metadata[key] = val
		}
		return
	}
	if arr, ok := existing.([]any); ok {
		metadata[key] = append(arr, val)
		return
	}
	metadata[key] = []any{existing, val}
}

func prefixedKey(name xml.Name) string {
	if name.Space == "" {
		return name.Local
	}
	prefix, ok := knownPrefixes[name.Space]
	if !ok {
		prefix = name.Space
	}
	return prefix + ":" + name.Local
}

// xmlAnyValue converts one extension element to a JSON-friendly value:
// attr-only → object of attrs; chardata-only → string; children → nested
// object/array; attrs+chardata → object with a "#text" key.
func xmlAnyValue(x xmlAny) any {
	obj := map[string]any{}
	for _, a := range x.Attrs {
		if a.Name.Local == "" {
			continue
		}
		obj[a.Name.Local] = a.Value
	}

	if len(x.Children) > 0 {
		childCollisions := map[string]map[string]bool{}
		for _, c := range x.Children {
			if _, ok := childCollisions[c.XMLName.Local]; !ok {
				childCollisions[c.XMLName.Local] = map[string]bool{}
			}
			childCollisions[c.XMLName.Local][c.XMLName.Space] = true
		}
		for _, c := range x.Children {
			key := c.XMLName.Local
			if len(childCollisions[key]) > 1 {
				key = prefixedKey(c.XMLName)
			}
			cVal := xmlAnyValue(c)
			_, isObject := cVal.(map[string]any)
			appendMetadata(obj, key, cVal, isObject)
		}
		return obj
	}

	text := strings.TrimSpace(x.Chardata)
	if len(obj) == 0 {
		return text
	}
	if text != "" {
		obj["#text"] = text
	}
	return obj
}

// --- helpers ---

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

var timeLayouts = []string{
	time.RFC3339,
	time.RFC3339Nano,
	time.RFC1123Z,
	time.RFC1123,
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02",
	"Mon, 02 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 02 Jan 2006 15:04:05 MST",
}

// normalizeTime parses a variety of RSS/Atom timestamp formats and returns
// RFC3339 in UTC. Unparseable values are returned trimmed but unchanged.
func normalizeTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return raw
}

// feedFilename derives a stable JSONL file name from a feed URL. It combines a
// human-readable slug (the last meaningful path segment, stripping index.*
// filenames) with a short hash of the full URL. The hash suffix guarantees two
// distinct feeds in the same project never collide onto one file — even when
// their paths slug to the same name (e.g. two hosts both serving
// /blog/index.xml) — which would otherwise merge unrelated entries by id.
func feedFilename(feedURL string) string {
	slug := feedSlug(feedURL)
	hash := shortHash(feedURL)
	if slug == "" {
		return "feed-" + hash + ".jsonl"
	}
	return slug + "-" + hash + ".jsonl"
}

func feedSlug(feedURL string) string {
	u, err := url.Parse(feedURL)
	if err != nil {
		return ""
	}

	segments := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })

	// Walk from the end, skipping index-style filenames, until a meaningful
	// segment is found.
	for i := len(segments) - 1; i >= 0; i-- {
		seg := strings.ToLower(segments[i])
		if isIndexSegment(seg) {
			continue
		}
		// Strip a file extension (everything from the first dot) so
		// "podcast.xml" becomes "podcast".
		if idx := strings.IndexByte(seg, '.'); idx >= 0 {
			seg = seg[:idx]
		}
		if s := slugify(seg); s != "" {
			return s
		}
	}

	// No path segment worked; slug the host.
	return slugify(u.Host)
}

func isIndexSegment(seg string) bool {
	base := seg
	if idx := strings.IndexByte(base, '.'); idx >= 0 {
		base = base[:idx]
	}
	return base == "index" || base == "" || base == "feed" || base == "rss" || base == "atom"
}

func slugify(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// shortHash returns a short hex digest of s, used to disambiguate feed
// filenames that would otherwise slug to the same name.
func shortHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return fmt.Sprintf("%x", sum[:4])
}
