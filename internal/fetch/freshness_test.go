package fetch

import (
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

func markPyPI(t *testing.T, dataDir, target, date string) {
	t.Helper()
	if err := store.MarkFetchedSeries(dataDir, "pypi", "proj", "", date, []string{target}); err != nil {
		t.Fatal(err)
	}
}

// A month waits for a lagging target until it's further behind than its
// freshness limit. A target that's no longer configured doesn't count.
func TestConcatenationWaitsForLaggingTargets(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{Projects: map[string]config.Project{"proj": {PyPI: config.StringList{"a", "b"}}}}
	now := time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC)
	markPyPI(t, dataDir, "a", "2026-10-31")
	markPyPI(t, dataDir, "b", "2026-10-30")
	markPyPI(t, dataDir, "removed", "2026-09-01")

	ready := func() bool {
		return ConcatenationPolicy(cfg, dataDir, now).Ready("metrics", "pypi", "proj", "2026-10-31")
	}
	if ready() {
		t.Error("ready while b is a day behind, within its limit")
	}
	markPyPI(t, dataDir, "b", "2026-10-31")
	if !ready() {
		t.Error("not ready once every configured target is past the month's end")
	}

	dataDir = t.TempDir()
	markPyPI(t, dataDir, "a", "2026-10-31")
	markPyPI(t, dataDir, "b", "2026-10-20")
	if !ready() {
		t.Error("a target further behind than its limit holds the project up")
	}
}

// A target without a watermark falls back to the project's stored data.
func TestConcatenationWithoutWatermarksUsesStoredData(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{Projects: map[string]config.Project{"proj": {PyPI: config.StringList{"a"}}}}
	now := time.Date(2026, 11, 8, 9, 30, 0, 0, time.UTC)
	policy := ConcatenationPolicy(cfg, dataDir, now)
	if policy.Day != config.DefaultConcatenateOnDay {
		t.Errorf("day = %d, want the default", policy.Day)
	}
	if policy.Ready("metrics", "pypi", "proj", "2026-10-31") {
		t.Error("ready without any stored data")
	}
	if _, err := store.WriteRecords(dataDir, "pypi", "proj", []source.Record{{Metric: "daily_downloads", ProjectID: "proj", Target: "a", Date: "2026-10-31", Value: 1}}); err != nil {
		t.Fatal(err)
	}
	if !ConcatenationPolicy(cfg, dataDir, now).Ready("metrics", "pypi", "proj", "2026-10-31") {
		t.Error("not ready with data stored through the month's end")
	}
}

func TestFreshness(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{Projects: map[string]config.Project{"proj": {PyPI: config.StringList{"a", "b", "new"}}}}
	markPyPI(t, dataDir, "a", "2026-10-19")
	markPyPI(t, dataDir, "b", "2026-10-10")
	got, err := Freshness(cfg, dataDir, time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]any{"a": {1, false}, "b": {10, true}, "new": {0, false}}
	if len(got) != len(want) {
		t.Fatalf("freshness = %+v", got)
	}
	for _, f := range got {
		if w := want[f.Target]; f.Lag != w[0] || f.Stale() != w[1] || f.Limit != 2 {
			t.Errorf("%s: lag %d, stale %v, limit %d; want %v", f.Target, f.Lag, f.Stale(), f.Limit, w)
		}
	}
}
