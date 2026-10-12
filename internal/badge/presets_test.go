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
	emittedSet := make(map[string]bool, len(EmittedMetrics))
	for _, m := range EmittedMetrics {
		emittedSet[m] = true
	}

	eventSet := make(map[string]bool, len(EmittedEventTypes))
	for _, e := range EmittedEventTypes {
		eventSet[e] = true
	}

	for name, p := range Presets {
		switch p.Table {
		case "metrics":
			// Each metrics preset Condition must mention at least one metric
			// in EmittedMetrics.
			found := false
			for _, m := range EmittedMetrics {
				if strings.Contains(p.Condition, "'"+m+"'") {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("preset %q references no known emitted metric in condition %q\nemitted metrics: %v",
					name, p.Condition, EmittedMetrics)
			}
		case "events":
			// Each events preset Condition must mention at least one type in
			// EmittedEventTypes.
			found := false
			for _, e := range EmittedEventTypes {
				if strings.Contains(p.Condition, "'"+e+"'") {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("preset %q references no known emitted event type in condition %q\nemitted event types: %v",
					name, p.Condition, EmittedEventTypes)
			}
		}
	}
}
