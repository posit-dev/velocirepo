package badge

import "fmt"

// Preset describes a built-in badge. Table is the DuckDB view to query.
// Condition is the metric/event filter without a WHERE keyword; it is always
// parenthesised before a project filter is appended so that OR conditions are
// not split by operator precedence. Aggregate is the SELECT expression that
// produces the numeric badge value.
//
// Query is a compatibility field that holds the base query without a project
// filter; it is kept for callers (e.g. the MCP handler) that have not yet
// been updated to call BuildQuery. Those callers should migrate to BuildQuery
// to get correct operator-precedence handling when a project filter is added.
type Preset struct {
	Label     string
	Table     string
	Condition string
	Aggregate string
	Color     string
	// Query is the base SQL query (no project filter). Prefer BuildQuery.
	Query string
}

// BuildQuery returns a complete SQL query for the preset. projectLiteral is a
// SQL string literal (e.g. "'my-proj'") to scope the result; pass "" for no
// project filter.
func (p Preset) BuildQuery(projectLiteral string) string {
	cond := "(" + p.Condition + ")"
	if projectLiteral != "" {
		cond += " AND project = " + projectLiteral
	}
	return fmt.Sprintf("SELECT COALESCE(%s, 0) AS value FROM %s WHERE %s",
		p.Aggregate, p.Table, cond)
}

// EmittedMetrics is the authoritative list of metric names that sources emit.
// A test uses this list to verify that every preset Condition references at
// least one known metric name.
var EmittedMetrics = []string{
	// PyPI, CRAN
	"daily_downloads",
	// OpenVSX
	"total_downloads",
	"total_reviews",
	"total_ratings",
	// Plausible site-level
	"daily_site_pageviews",
	"daily_site_visitors",
	"daily_site_visits",
	// Plausible page-level
	"daily_pageviews",
	"daily_visitors",
	"daily_visits",
	// YouTube
	"total_views",
	"total_likes",
	"total_comments",
	"total_subscribers",
}

// EmittedEventTypes is the authoritative list of event types that sources emit.
var EmittedEventTypes = []string{
	"star", "fork", "issue_open", "issue_close", "pr_open", "pr_merge", "comment",
}

var Presets map[string]Preset

func init() {
	raw := []Preset{
		{
			Label:     "stars",
			Table:     "events",
			Condition: "type = 'star'",
			Aggregate: "COUNT(*)",
			Color:     "#007ec6",
		},
		{
			Label:     "forks",
			Table:     "events",
			Condition: "type = 'fork'",
			Aggregate: "COUNT(*)",
			Color:     "#007ec6",
		},
		// downloads: cumulative sum of daily_downloads (PyPI, CRAN). OpenVSX
		// extension downloads are change-point encoded under total_downloads
		// and can be queried with a custom badge.
		{
			Label:     "downloads",
			Table:     "metrics",
			Condition: "metric = 'daily_downloads'",
			Aggregate: "SUM(value)",
			Color:     "#44cc11",
		},
		// pageviews: cumulative sum of site-level daily pageviews (Plausible).
		{
			Label:     "pageviews",
			Table:     "metrics",
			Condition: "metric = 'daily_site_pageviews'",
			Aggregate: "SUM(value)",
			Color:     "#44cc11",
		},
	}

	Presets = make(map[string]Preset, len(raw))
	for _, p := range raw {
		// Populate Query from BuildQuery for callers that haven't yet
		// migrated to BuildQuery (e.g. the MCP handler).
		p.Query = p.BuildQuery("")
		Presets[p.Label] = p
	}
}
