package datadict

// What every velocirepo export has, whatever its sources.

var tableDescriptions = map[string]string{
	"projects": "One row per project in velocirepo.toml: its id, name, description, tags and website. " +
		"Use it to resolve informal names to the project id that every other table uses.",
	"targets": "One row per project, source and target in velocirepo.toml. " +
		"It says which sources cover a project, including targets without data yet, and which copy counts when more than one project tracks a target.",
	"metrics": "Daily time series of every source: one row per project, source, target, metric and date, " +
		"and per page, video, post or issue for the series that have one. " +
		"Includes the daily counts velocirepo derives from the events table.",
	"metrics_filled": "Like metrics, but with every total_* series carried forward to every day through the latest day fetched. " +
		"The table for any total_* question: the value on a date, or the latest value.",
	"indicators": "Momentum of every daily_* series, computed over the metrics table: one row per project, source, target, metric, date and indicator.",
	"events": "Individual events with a timestamp, the acting user and the issue or pull request they happened on. " +
		"Use it for per-user or sub-daily detail; for counts over time, use the daily_* metrics derived from it.",
	"content": "Catalog of the things the metrics count, such as issues, pull requests, repositories, videos, posts and feed items: " +
		"one row per item, with its title, text, timestamps and URL.",
	"meta": "One row: as_of, the latest day of the metrics, normally yesterday, and built_at, when this export was made. " +
		"Check it before answering a question about the current or latest values.",
}

var tableDetails = map[string]string{
	"metrics": "Metric names say how values combine: daily_* values are amounts per day and can be summed over days; " +
		"total_* values are running totals or snapshots, stored only on the days they change, so never sum them over days: " +
		"read them from metrics_filled or take the increase over a range; " +
		"window metrics (such as downloads_30d) are rolling-window snapshots stored on every day they're fetched, so read the latest value. " +
		"A series starting on some date usually means collection started then, not that the project started then.",
	"metrics_filled": "daily_* rows pass through unchanged and aren't filled. " +
		"A total_* series keyed by a page, video, post or issue is filled per key.",
	"indicators": "growth_rate compares the sum of the trailing 28 rows with the 28 rows before them ((sum - prior sum) / prior sum); " +
		"trend is the per-day regression slope over the trailing 28 rows. " +
		"Windows count rows, not days, so where a series leaves out days without a value they span more than 28 days.",
	"content": "description and content can hold whole issue bodies and posts: search them with LIKE and select them only for a few rows. " +
		"Content is updated at every fetch, so a state reflects the latest fetch, not the as-of date.",
}

var columnDescriptions = map[string]map[string]string{
	"projects": {
		"id":          "Project id, used as project in every other table.",
		"name":        "Display name.",
		"description": "Short description.",
		"color":       "Display color, as a hex code.",
		"tags":        "Language or topic tags.",
		"website":     "Website URL.",
		"logo":        "Logo URL or path.",
	},
	"targets": {
		"project":   "Project id.",
		"source":    "Source of the target.",
		"target":    "The source-specific identifier, such as a package name, repository or site.",
		"canonical": "False for a copy of a target that another project keeps: counting only canonical rows counts every target once.",
	},
	"metrics": {
		"project": "Project id.",
		"source":  "Source of the value.",
		"target":  "The source-specific identifier the value is about, such as a package name, repository or site.",
		"metric":  "What the value measures. The prefix says how values combine over days: daily, total or a rolling window.",
		"date":    "The day the value describes, in the source's time zone.",
		"value":   "The value: an amount for the day (daily_*), a running total or snapshot (total_*) or a rolling-window count.",
		"extra":   "All source-specific fields of the row as JSON text, including those that are columns of their own.",
	},
	"indicators": {
		"metric":    "The daily_* metric the indicator is computed over.",
		"indicator": "Which statistic the row holds.",
		"date":      "The day the trailing windows end.",
		"value":     "growth_rate is a proportion (0.1 is up 10% on the prior window); trend is in metric units per day.",
	},
	"events": {
		"project":  "Project id.",
		"source":   "Source of the event.",
		"type":     "What happened.",
		"target":   "The repository the event happened in.",
		"datetime": "When it happened.",
		"ref":      "The issue or pull request number the event happened on; NULL for events on the repository, such as forks.",
		"user":     "Login of the acting user; NULL where the source no longer reports one, such as a deleted account. See the type for whose login it is.",
		"extra":    "Source-specific fields as JSON text.",
	},
	"content": {
		"project":      "Project id.",
		"source":       "Source of the item.",
		"target":       "The target the item came from, as in metrics.",
		"id":           "Source-specific id of the item; unique within a project, source and target.",
		"ref":          "Issue or pull request number; NULL for other items.",
		"title":        "Title.",
		"description":  "Body, summary or description. Long text.",
		"content":      "Full text, where the source has it, with HTML removed. Long text.",
		"published_at": "When the item was created or published.",
		"updated_at":   "When the item last changed, where the source reports it.",
		"url":          "Link to the item.",
		"duration":     "Length of a video, in seconds.",
		"tags":         "Labels, topics or keywords.",
		"type":         "What kind of item the row is.",
		"extra":        "Other source-specific fields as JSON text, which differ per source and type. Look at a row before relying on a key.",
	},
	"meta": {
		"as_of":    "The latest day of the metrics.",
		"built_at": "When the export was made.",
	},
}

// The content states velocirepo stores.
var stateLabels = map[string]string{
	"OPEN":   "open",
	"CLOSED": "closed; for a pull request, without merging",
	"MERGED": "merged pull request",
}

const datasetDetails = "Route questions by table: metrics for daily series; metrics_filled for anything total_*; " +
	"indicators for 28-day growth and trend; events for per-user or per-timestamp detail; " +
	"content for the titles, text, authors and states behind the counts; projects and targets to resolve project names and see which sources cover a project. " +
	"Sources count differently, so compare levels across sources with caution."

var glossary = []Value{
	{"project", "A tracked open source project, identified by projects.id. One project can be measured by several sources; the source column tells them apart."},
	{"source vs target", "source is where a value came from, such as cran or github; target is the source-specific identifier there, such as a package name, repository, site or feed URL."},
	{"daily_* vs total_*", "daily_* is an amount per day, which can be summed over days; total_* is a running total or snapshot, which can't: use metrics_filled, or the difference between two days."},
}
