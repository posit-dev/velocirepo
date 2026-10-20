package badge

import (
	"strings"
	"testing"
)

// TestPresetsBuildQueryNoProject verifies that BuildQuery without a project
// filter produces valid SQL (no AND dangling operator) for every preset.
func TestPresetsBuildQueryNoProject(t *testing.T) {
	for name, p := range Presets {
		q := p.BuildQuery("")
		if !strings.Contains(q, "SELECT COALESCE(") {
			t.Errorf("preset %q: query missing COALESCE: %s", name, q)
		}
		if !strings.Contains(q, "FROM "+p.Table) {
			t.Errorf("preset %q: query missing FROM %s: %s", name, p.Table, q)
		}
		// Condition must be wrapped in parens.
		if !strings.Contains(q, "("+p.Condition+")") {
			t.Errorf("preset %q: condition not parenthesised: %s", name, q)
		}
		// No stray AND at the end.
		trimmed := strings.TrimSpace(q)
		if strings.HasSuffix(trimmed, "AND") {
			t.Errorf("preset %q: query ends with AND: %s", name, q)
		}
	}
}

// TestPresetsBuildQueryWithProject verifies that a project filter is always
// added after the parenthesised condition, not spliced into an OR expression.
func TestPresetsBuildQueryWithProject(t *testing.T) {
	for name, p := range Presets {
		q := p.BuildQuery("'my-proj'")
		want := "(" + p.Condition + ") AND project = 'my-proj'"
		if !strings.Contains(q, want) {
			t.Errorf("preset %q: project filter not correctly placed\ngot:  %s\nwant substring: %s", name, q, want)
		}
	}
}

// TestPresetsReferencesKnownMetrics fails if a metric-table preset references a
// metric name that no source emits, preventing silent regressions when sources
// change their metric names.
func TestPresetsReferencesKnownMetrics(t *testing.T) {
	emittedSet := make(map[string]bool, len(emittedMetrics))
	for _, m := range emittedMetrics {
		emittedSet[m] = true
	}

	eventSet := make(map[string]bool, len(emittedEventTypes))
	for _, e := range emittedEventTypes {
		eventSet[e] = true
	}

	for name, p := range Presets {
		switch p.Table {
		case "metrics":
			// Each metrics preset Condition must mention at least one metric
			// in emittedMetrics.
			found := false
			for _, m := range emittedMetrics {
				if strings.Contains(p.Condition, "'"+m+"'") {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("preset %q references no known emitted metric in condition %q\nemitted metrics: %v",
					name, p.Condition, emittedMetrics)
			}
		case "events":
			// Each events preset Condition must mention at least one type in
			// emittedEventTypes.
			found := false
			for _, e := range emittedEventTypes {
				if strings.Contains(p.Condition, "'"+e+"'") {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("preset %q references no known emitted event type in condition %q\nemitted event types: %v",
					name, p.Condition, emittedEventTypes)
			}
		}
	}
}

// emittedMetrics lists the metric names that sources emit today. A preset that
// filters on a name not in this list would always render 0.
var emittedMetrics = []string{
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
	// GitHub
	"total_reactions",
	"total_stars",
}

// emittedEventTypes lists the event types that sources emit today.
var emittedEventTypes = []string{
	"star", "fork", "issue_open", "issue_close", "pr_open", "pr_merge", "comment",
}
