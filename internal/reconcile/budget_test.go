package reconcile

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/fetch"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

func TestBudget(t *testing.T) {
	b := NewBudget(map[string]fetch.Quota{"api.github.com/graphql": {Limit: 5000, Remaining: 4000}})
	if !b.Allow("api.github.com/graphql", 400) || !b.Allow("api.github.com/graphql", 100) {
		t.Error("10% of the quota wasn't allowed")
	}
	if b.Allow("api.github.com/graphql", 1) {
		t.Error("more than 10% of the quota was allowed")
	}
	low := NewBudget(map[string]fetch.Quota{"api.github.com/graphql": {Limit: 5000, Remaining: 520}})
	if low.Allow("api.github.com/graphql", 30) {
		t.Error("the last 10% of the quota was used")
	}
	s := NewBudget(nil)
	if !s.Allow(SearchQuota, SearchBudget) || s.Allow(SearchQuota, 1) {
		t.Error("the search budget isn't 270")
	}
}

// Over seven nights, every repository's last complete month is checked
// exactly once, and no night exceeds the search budget.
func TestRotation(t *testing.T) {
	targets := make([]string, 465)
	for i := range targets {
		targets[i] = fmt.Sprintf("org/repo%03d", i)
	}
	seen := map[string]int{}
	start := time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)
	for n := 0; n < 7; n++ {
		tonight := Tonight(targets, start.AddDate(0, 0, n))
		if searches := 4 * len(tonight); searches > SearchBudget {
			t.Errorf("night %d needs %d searches", n, searches)
		}
		for _, tg := range tonight {
			seen[tg]++
		}
	}
	for _, tg := range targets {
		if seen[tg] != 1 {
			t.Fatalf("%s was checked %d times in a week", tg, seen[tg])
		}
	}
}

func TestPacer(t *testing.T) {
	now := time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)
	var slept time.Duration
	p := NewSearchPacer()
	p.Now = func() time.Time { return now }
	p.Sleep = func(_ context.Context, d time.Duration) error { slept += d; now = now.Add(d); return nil }
	for i := 0; i < 31; i++ {
		_ = p.Wait(context.Background())
	}
	if slept != time.Minute {
		t.Errorf("31 searches took %s, want a minute for the last 30", slept)
	}
}

// monthly gives canned per-month totals.
type monthly struct{ totals []source.ReferenceTotal }

func (monthly) ReferenceTotals(context.Context, []string) ([]source.ReferenceTotal, error) {
	return nil, nil
}
func (m monthly) MonthlyRequests() int { return 4 }
func (m monthly) MonthlyReferenceTotals(_ context.Context, targets []string, from, through time.Time, _ func(context.Context) error) ([]source.ReferenceTotal, error) {
	var out []source.ReferenceTotal
	for _, t := range m.totals {
		for _, tg := range targets {
			if t.Target == tg {
				t.From, t.Through = from, through
				out = append(out, t)
			}
		}
	}
	return out, nil
}

// Month boundaries are compared in UTC, the time zone the events are stored
// in, so items near midnight don't cause false differences; a missing week
// that all-time totals wouldn't see is found.
func TestMonthlyWindows(t *testing.T) {
	dataDir := t.TempDir()
	var events []source.Event
	for i, dt := range []string{"2026-08-31T23:59:59Z", "2026-09-01T00:00:00Z", "2026-09-15T12:00:00Z", "2026-09-30T23:59:59Z", "2026-10-01T00:00:00Z"} {
		ref := i + 1
		events = append(events, source.Event{Type: "issue_open", ProjectID: "p", Target: "org/repo", Datetime: dt, Ref: &ref})
	}
	if _, err := store.WriteEvents(dataDir, "github", "p", events); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Projects: map[string]config.Project{"p": {GitHubEvents: config.StringList{"org/repo"}}}}
	// A night on which org/repo is in the rotation.
	now := time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)
	for len(Tonight([]string{"org/repo"}, now)) == 0 {
		now = now.AddDate(0, 0, 1)
	}
	run := func(value int64) []Result {
		rec := monthly{totals: []source.ReferenceTotal{{Target: "org/repo", Kind: "events", Metric: "issue_open", Label: "issues opened", Value: value, Tolerance: 0}}}
		results, _, err := Run(context.Background(), cfg, dataDir, Options{Now: now, Wait: func(context.Context) error { return nil }}, map[string]source.Reconciler{"github": rec})
		if err != nil {
			t.Fatal(err)
		}
		return results
	}
	results := run(3)
	if len(results) != 1 || results[0].Stored != 3 || results[0].Differs() {
		t.Errorf("results = %+v, want September's 3 issues to match", results)
	}
	if results := run(10); !results[0].Differs() {
		t.Errorf("a missing week in September wasn't found: %+v", results)
	}
}
