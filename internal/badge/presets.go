package badge

import "fmt"

// Preset describes a built-in badge. Table is the DuckDB view to query,
// Condition the metric or event filter (without WHERE), and Aggregate the
// SELECT expression that produces the badge value.
type Preset struct {
	Label     string
	Table     string
	Condition string
	Aggregate string
	Color     string
}

// BuildQuery returns the SQL query for the preset. projectLiteral is a SQL
// string literal (e.g. "'my-proj'") that scopes the result to one project;
// pass "" for all projects. The condition is parenthesised so that a project
// filter can't be split by operator precedence, and a missing value is 0.
func (p Preset) BuildQuery(projectLiteral string) string {
	cond := "(" + p.Condition + ")"
	if projectLiteral != "" {
		cond += " AND project = " + projectLiteral
	}
	return fmt.Sprintf("SELECT COALESCE(%s, 0) AS value FROM %s WHERE %s",
		p.Aggregate, p.Table, cond)
}

var Presets = map[string]Preset{
	"stars": {
		Label:     "stars",
		Table:     "events",
		Condition: "type = 'star'",
		Aggregate: "COUNT(*)",
		Color:     "#007ec6",
	},
	"forks": {
		Label:     "forks",
		Table:     "events",
		Condition: "type = 'fork'",
		Aggregate: "COUNT(*)",
		Color:     "#007ec6",
	},
	// downloads: all-time sum of daily_downloads (PyPI, CRAN). OpenVSX
	// downloads are a change-point encoded total_downloads series; use a
	// custom badge for those.
	"downloads": {
		Label:     "downloads",
		Table:     "metrics",
		Condition: "metric = 'daily_downloads'",
		Aggregate: "SUM(value)",
		Color:     "#44cc11",
	},
	// pageviews: all-time sum of site-level daily pageviews (Plausible).
	"pageviews": {
		Label:     "pageviews",
		Table:     "metrics",
		Condition: "metric = 'daily_site_pageviews'",
		Aggregate: "SUM(value)",
		Color:     "#44cc11",
	},
}
