# Plan: RSS/Atom content fetcher for velocirepo

## Goal

Add a `source.Source` that reads a syndication feed (RSS 2.0 **or** Atom), and
writes each feed item as a `ContentEntry` JSONL line under
`data/content/rss/<project-id>/<name>.jsonl`. Also emit a `total_items` metric
per feed fetch. Works for our own custom Atom feeds (rich `vr:` metadata + raw
Markdown body) **and** arbitrary third-party feeds (podcasts, blogs, etc.).

## Design principles

1. **Format-agnostic core model.** Parse RSS and Atom into one internal
   `feedItem` shape, then map to `ContentEntry`. Callers never see the format.
2. **Standard elements → typed fields; everything else → `metadata`.** The
   RSS/Atom specs define a fixed set of core elements. Anything outside that set
   is, by spec, a namespaced extension element (`vr:`, `itunes:`, `dc:`,
   `media:`, …). We capture all extension elements generically and route them to
   `metadata`. We do **not** special-case our own `vr:` namespace — our feed is
   just one feed-with-extensions among many.
3. **Body is first-class.** `<content>` (Atom) / `<content:encoded>` or
   `<description>` (RSS) → the new top-level `content` field, **not** metadata.
4. **Upsert by id.** Reuse the existing content merge-by-id in
   `store.WriteContent`, so re-fetching updates changed lines in place. The
   feed's per-entry `<updated>` drives change detection downstream.

---

## JSONL shape

Extend `source.ContentEntry` (in `internal/source/content.go`) with one field:

```go
type ContentEntry struct {
    Source      string         `json:"source"`
    ProjectID   string         `json:"project_id"` // stamped by store.WriteContent
    Target      string         `json:"target"`
    ID          string         `json:"id"`
    Title       string         `json:"title"`
    Description string         `json:"description,omitempty"`
    Content     string         `json:"content,omitempty"`    // NEW: body (Atom <content> / RSS <content:encoded>|<description>)
    PublishedAt string         `json:"published_at"`
    UpdatedAt   string         `json:"updated_at,omitempty"` // NEW: Atom <updated> / RSS <lastBuildDate>-equivalent; drives freshness
    URL         string         `json:"url,omitempty"`
    Duration    *int64         `json:"duration,omitempty"`
    Tags        []string       `json:"tags,omitempty"`
    Type        string         `json:"type,omitempty"`
    Metadata    map[string]any `json:"metadata,omitempty"`
}
```

Notes:
- `Content` and `UpdatedAt` are additive and `omitempty`, so existing YouTube /
  LinkedIn JSONL is unaffected and existing tests keep passing.
- `Description` = the short summary (`<summary>` / RSS `<description>` when a
  separate `<content:encoded>` exists). `Content` = the full body. If a feed has
  only `<description>` and no separate body, it goes to `Description` and
  `Content` stays empty (see "Ambiguities" below — this is a decision point).

### Example line (our Atom blog feed)

```json
{
  "source": "rss",
  "project_id": "open-source-website",
  "target": "https://opensource.posit.co/blog/index.md.xml",
  "id": "https://opensource.posit.co/blog/2026-07-23_ir-0-1-0/",
  "title": "ir 0.1.0: self-describing R scripts and Quarto documents",
  "description": "`ir` is a new command-line tool for running portable R scripts…",
  "content": "## Introduction\n\n`ir` lets you…  (raw markdown)",
  "published_at": "2026-07-23T00:00:00Z",
  "updated_at": "2026-07-23T21:03:49Z",
  "url": "https://opensource.posit.co/blog/2026-07-23_ir-0-1-0/",
  "tags": ["Reproducibility", "CLI"],
  "type": "post",
  "metadata": {
    "authors": [
      {"name": "Tomasz Kalinowski", "uri": "https://opensource.posit.co/people/tomasz-kalinowski/"}
    ],
    "software": [
      {"term": "pak", "href": "https://opensource.posit.co/software/pak/"}
    ],
    "language": [
      {"term": "R", "href": "https://opensource.posit.co/languages/r/"}
    ],
    "topic": [
      {"term": "Best Practices", "href": "https://opensource.posit.co/topics/best-practices/"}
    ],
    "image": {"href": "https://…/thumbnail.png", "alt": "…"}
  }
}
```

### Example line (hypothetical third-party podcast feed)

```json
{
  "source": "rss",
  "project_id": "some-project",
  "target": "https://example.com/podcast.xml",
  "id": "https://example.com/ep/42",
  "title": "Episode 42",
  "content": "<p>Show notes…</p>",
  "published_at": "2026-05-01T08:00:00Z",
  "url": "https://example.com/ep/42",
  "type": "episode",
  "metadata": {
    "itunes:duration": "3600",
    "itunes:episode": "42",
    "dc:creator": "Jane Doe"
  }
}
```

Third-party extension elements land in `metadata` automatically, prefixed by
namespace when there's a collision with a core field or another namespace.

---

## Mapping tables

### Atom → ContentEntry

| Atom element | ContentEntry |
|---|---|
| `<id>` | `ID` |
| `<title>` | `Title` |
| `<summary>` | `Description` |
| `<content>` | `Content` |
| `<published>` | `PublishedAt` (normalized RFC3339) |
| `<updated>` | `UpdatedAt` |
| `<link rel="alternate">` href | `URL` |
| `<category term>` | `Tags` (append each term) |
| `<author>` (name+uri) | `Metadata["authors"]` = `[]{name,uri}` |
| any other-namespace element (`vr:*`, `itunes:*`, …) | `Metadata[...]` |

Our `vr:type` is special-cased to fill `Type` (post/event/person/software/
resource) rather than living in metadata — it's the closest analog to
YouTube/LinkedIn's `Type`. All other `vr:*` → metadata.

### RSS 2.0 → ContentEntry

| RSS element | ContentEntry |
|---|---|
| `<guid>` (or `<link>` if no guid) | `ID` |
| `<title>` | `Title` |
| `<description>` | `Description` (or `Content` — see Ambiguities) |
| `<content:encoded>` | `Content` |
| `<pubDate>` (RFC1123Z) | `PublishedAt` (normalized RFC3339) |
| `<atom:updated>` / `<dc:date>` if present | `UpdatedAt` |
| `<link>` | `URL` |
| `<category>` | `Tags` |
| `<author>` / `<dc:creator>` | `Metadata["authors"]` |
| any other-namespace element | `Metadata[...]` |

---

## Extension-element capture (the generic mechanism)

Go's `encoding/xml` supports a catch-all: declare known fields explicitly, then
collect the rest.

```go
type atomEntry struct {
    ID        string       `xml:"id"`
    Title     string       `xml:"title"`
    Summary   string       `xml:"summary"`
    Content   atomText     `xml:"content"`
    Published string       `xml:"published"`
    Updated   string       `xml:"updated"`
    Links     []atomLink   `xml:"link"`
    Cats      []atomCat    `xml:"category"`
    Authors   []atomAuthor `xml:"author"`
    Extra     []xmlAny     `xml:",any"`   // everything else, any namespace
}

type xmlAny struct {
    XMLName  xml.Name
    Attrs    []xml.Attr `xml:",any,attr"`
    Chardata string     `xml:",chardata"`
    Children []xmlAny   `xml:",any"`
}
```

`,any` captures every element not matched by an explicit field, **including its
namespace** (via `XMLName.Space`) and attributes. We then walk `Extra` and
build metadata values:

- Cardinality is chosen by element **name**, not structure. A small
  known-plural set (`software`, `language`, `topic`) models repeatable
  taxonomy-like relations and is **always** a JSON array — even a single
  occurrence — so the type stays consistent whether an entry has one or several.
  Each object is the element's attributes: `{"term":"pak","href":"…"}`.
- Every other element (e.g. `<vr:image .../>`, `<vr:source>tidyverse</vr:source>`,
  `<itunes:duration>`) is scalar (its object or string value) and only promotes
  to an array if that same element actually repeats within the entry.
- Elements with children → nested object/array recursively.
- **Key** = local name (`software`). On collision across namespaces, fall back
  to `prefix:local` using a small known-namespace prefix map
  (`vr`, `itunes`, `dc`, `media`, `content`, `atom`), else the namespace URI.

This is the single code path that handles our `vr:` fields and any third-party
extension. Our `authors` are handled explicitly (promoted from `<author>`),
everything else flows through the generic walker.

---

## Files to add / change

### New: `internal/source/rss.go`
- `type RSS struct { Client *http.Client; FeedURL string; ... contentEntries []ContentEntry; totalItems int }`
- `Name() string { return "rss" }`
- `Fetch(ctx, opts) ([]Record, error)`:
  1. GET the feed URL (reuse `doRequest` from `http_json.go`; add an XML variant
     or just read bytes and `xml.Unmarshal`).
  2. Sniff root element: `<rss>` vs `<feed>` → parse with the right struct.
  3. Build `contentEntries`.
  4. Return one `Record`: `metric="total_items"`, `value=len(items)`,
     `date=opts.EndDate`, `target=FeedURL`.
- Implements `ContentProvider`:
  - `ContentEntries() []ContentEntry`
  - `ContentFilename() string` — derived from the feed (see "filename" below).

### New: `internal/source/rss_test.go`
- httptest server serving canned Atom + RSS fixtures (small handwritten ones,
  plus optionally a trimmed real sample from our feed).
- Assert: format detection, core field mapping, body → `content`, `vr:*` →
  metadata (arrays, attr-objects, scalars), authors promoted, `total_items`
  record, third-party namespace passthrough, date normalization, upsert.

### Change: `internal/source/content.go`
- Add `Content` and `UpdatedAt` fields (above).

### Change: `internal/sourceinfo/sources.go`
- Add a descriptor:
  ```go
  {
    Name: "rss", DisplayName: "RSS/Atom",
    Category: CategoryMetrics,          // emits total_items metric
    ContentDir: "content/rss",
    ConfigField: "RSS", TOMLKey: "rss", CLIFlag: "rss",
    CLIUsage: "RSS or Atom feed URL",
    AddPrompt: "RSS/Atom feed URL", UpdatePrompt: "RSS/Atom feed URL",
    JSONKeys: []string{"rss"}, CSVColumns: []string{"rss"},
    MCPKey: "rss",
    MCPAddDescription: "RSS or Atom feed URL",
    MCPUpdateDescription: "RSS or Atom feed URL (empty to remove)",
    FetchToolName: "fetch_rss",
    FetchDescription: "Fetch items from an RSS or Atom feed as content entries, plus a total_items metric.",
  }
  ```

### Change: `internal/config/config.go`
- Add `RSS StringList \`toml:"rss"\`` to `Project` (StringList → multiple feeds
  per project, consistent with all other sources).

### Change: `internal/fetch/fetch.go`
- Add a `fetchSourceDescriptor` entry with a `metricFactory`:
  ```go
  {
    Descriptor: sourceinfo.Must("rss"),
    metricFactory: func(client *http.Client, _ Tokens, target string) source.Source {
      return &source.RSS{Client: client, FeedURL: target}
    },
  },
  ```
  Content flows through the existing `runMetricJob` ContentProvider path — no
  new plumbing.

### Change: `cmd/velocirepo/cmd/fetch.go` (+ any fetch-tool table)
- Register a `fetch_rss` subcommand mirroring the others (thin wrapper around
  `fetch.SourceByName(..., "rss", ...)`), and MCP tool if the MCP layer
  enumerates sources (it appears to, via sourceinfo — verify `mcp/handlers.go`).

### Config data: add feeds to `open-source-website` in `velocirepo.toml`
```toml
[projects.open-source-website]
# … existing …
rss = [
  "https://opensource.posit.co/people/index.md.xml",
  "https://opensource.posit.co/blog/index.md.xml",
  "https://opensource.posit.co/events/index.md.xml",
  "https://opensource.posit.co/resources/index.md.xml",
  "https://opensource.posit.co/software/index.md.xml",
]
```

---

## Content filename (one file per feed)

Multiple feeds per project must not collide in
`data/content/rss/open-source-website/`. Options:

- **Chosen:** derive from the feed URL path — e.g.
  `https://…/blog/index.md.xml` → `blog.jsonl`, `people/index.md.xml` →
  `people.jsonl`. Use the last meaningful path segment (strip `index.md.xml` /
  `index.xml`, fall back to a slug of the host+path). This yields human-readable
  files: `blog.jsonl`, `events.jsonl`, `people.jsonl`, `resources.jsonl`,
  `software.jsonl`.
- Guard against collisions (two feeds slugging to the same name) by falling back
  to a hash suffix.

`ContentFilename()` computes this from `FeedURL`.

---

## total_items metric

- One `Record` per fetch: `source="rss"`, `metric="total_items"`,
  `target=FeedURL`, `date=EndDate`, `value=len(items)`.
- Because `target` is the feed URL, each of the five feeds gets its own daily
  time series (e.g. blog item count over time), aggregated by the normal
  metrics pipeline. Good for spotting growth / accidental truncation.

---

## Project identity (JSONL field) — DONE

`ContentEntry` now carries `project_id` as a top-level JSONL field (matching
`source.Record` / `source.Event`). The project also remains in the **path**
(`data/content/<source>/<project-id>/<name>.jsonl`); the two are kept in sync
because `store.WriteContent` **stamps** `project_id` from its `projectID`
argument onto every merged line before writing. This means:

- All content sources (YouTube, LinkedIn, future RSS) record `project_id`
  uniformly with zero per-fetcher code — the stamp happens in the shared write
  path.
- Pre-existing content lines are **backfilled** on the next write.
- `store.RewriteProjectID` already walks every `*.jsonl` and rewrites
  `project_id`, so project renames now keep content lines consistent for free.

DB views updated (`createContentViewRelative` in both `build_db.go` and
`duckdb.go`, plus `createEmptyContentView`): the content view now exposes
`project_id AS project` (matching metrics/events) via the explicit `columns=`
schema. Validation (`validateContentFile`) flags empty or directory-mismatched
`project_id` as a **fixable** `IssueProjectMismatch`, with
`store.FixProjectMismatches` backfilling from the parent directory name.

Still to add to the view when the RSS fields land: `content` (VARCHAR) and
`updated_at` (VARCHAR → `CAST(... AS TIMESTAMP)`) in both the `columns=` map and
the `SELECT` list — the explicit schema drops any key not listed.

## Ambiguities / decisions to confirm

1. **RSS `<description>` — summary or body? [RESOLVED]** Map faithfully and
   never combine: Atom `<summary>`/`RSS <description>` → `Description`;
   Atom `<content>`/`RSS <content:encoded>` → `Content`. When an RSS item has
   only `<description>` and no `<content:encoded>`, keep it literal
   (`description → Description`, `Content` empty). Downstream convention for
   "the fullest text": `coalesce(content, description)`. `Description` is an
   existing top-level `ContentEntry` field (LinkedIn already populates it); no
   new key introduced for it — only `content` and `updated_at` are new.
2. **Metadata key on namespace collision. [RESOLVED]** Default to the bare
   local name (`software`, `duration`). Only fall back to a prefixed key
   (`itunes:duration`) when two different namespaces share the same local name
   within one entry. Prefix comes from a small known-namespace map
   (`vr`, `itunes`, `dc`, `media`, `content`, `atom`); otherwise use the
   namespace URI. Keeps our own feeds clean while staying unambiguous for
   arbitrary third-party feeds.
3. **`vr:type` promotion to `Type`. [RESOLVED — yes]** The feed's `vr:type`
   (post/event/person/software/resource) populates the top-level `Type` field,
   consistent with YouTube `video` / LinkedIn `post`, so `WHERE type = '…'`
   works uniformly across all content sources. This is the one documented
   exception to "all `vr:*` → metadata"; every other `vr:*` element still flows
   through the generic extension walker into `metadata`. Third-party feeds with
   no `vr:type` simply leave `Type` empty.
4. **Body size.** Our feeds embed the full raw Markdown — JSONL lines can get
   large (blog posts). Fine for now (YouTube/LinkedIn also store text), just
   noting it. No truncation planned.
5. **HTTP conditional fetch.** Could send `If-None-Match`/`If-Modified-Since`
   and skip on 304 later. Out of scope for v1; fetch always re-parses.

---

## Test strategy (offline, per project conventions)

- All tests use `net/http/httptest` with canned Atom + RSS bodies. No network.
- Fixtures: (a) a minimal Atom entry with `vr:` extensions + authors + body;
  (b) a minimal RSS 2.0 item with `content:encoded`, `itunes:`/`dc:` extensions;
  (c) a malformed/empty feed (error path). Optionally commit a trimmed real
  sample from each of the five feeds under `internal/source/testdata/`.
- Assertions cover: format sniffing, every mapping-table row, generic extension
  capture (array vs object vs scalar), author promotion, date normalization to
  RFC3339, `total_items` record, filename derivation, and upsert-by-id via
  `store.WriteContent`.

---

## Rollout order

1. Extend `ContentEntry` (+ keep existing tests green).
2. `rss.go` + `rss_test.go` (the parser + mapping — the bulk).
3. Wire descriptor + config field + fetch factory + CLI/MCP.
4. Add the five feeds to `velocirepo.toml`.
5. Manual smoke test: `velocirepo fetch_rss --project open-source-website`
   against the live feeds; inspect `data/content/rss/open-source-website/*.jsonl`.
6. `go test ./...`.
```
