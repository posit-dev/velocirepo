package gate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/check"
	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/fetch"
	"github.com/posit-dev/velocirepo/internal/reconcile"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

// The incidents of the design's "Why", each as a scenario that must be
// reported, and must leave the stored data as it was. Each scenario names
// the check that catches it: with that check disabled, the scenario must
// fail, which is the mutation test.

// incident runs a scenario with the given checks, and reports whether it was
// caught and the data kept.
type incident struct {
	name  string
	check string
	run   func(t *testing.T, checks []check.Checker) (caught, kept bool)
}

func without(name string) []check.Checker {
	var out []check.Checker
	for _, c := range check.Default() {
		if c.Name() != name {
			out = append(out, c)
		}
	}
	return out
}

func has(findings []check.Finding, checkName string, sev check.Severity) bool {
	for _, f := range findings {
		if f.Check == checkName && f.Severity == sev {
			return true
		}
	}
	return false
}

// referenceFor gives canned reference totals, all-time and per month.
type referenceFor struct {
	allTime []source.ReferenceTotal
	monthly []source.ReferenceTotal
}

func (r referenceFor) ReferenceTotals(context.Context, []string) ([]source.ReferenceTotal, error) {
	return r.allTime, nil
}
func (r referenceFor) MonthlyRequests() int { return 1 }
func (r referenceFor) MonthlyQuota() string { return "" }
func (r referenceFor) MonthlyReferenceTotals(_ context.Context, targets []string, from, through time.Time, _ func(context.Context) error) ([]source.ReferenceTotal, error) {
	var out []source.ReferenceTotal
	for _, m := range r.monthly {
		m.From, m.Through = from, through
		out = append(out, m)
	}
	return out, nil
}

func (w *world) gateNight(checks []check.Checker, enforce bool, recs map[string]source.Reconciler, fetchFn func(ctx context.Context, o fetch.Options) ([]fetch.Result, error)) Outcome {
	w.t.Helper()
	day := w.day
	w.day = w.day.AddDate(0, 0, 1)
	if enforce {
		w.cfg.Settings.Gate = "enforce"
	}
	out, err := Run(context.Background(), Options{Config: w.cfg, GitEnv: gitEnv, NoConcatenate: true, Now: func() time.Time { return day },
		Checks: checks, Reconcilers: recs, Fetch: fetchFn, Repo: "org/data", APIURL: w.api.URL, RepoToken: "workflow-token"})
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

func fileOf(w *world, rel string) string {
	data, _ := os.ReadFile(filepath.Join(w.cfg.DataDir(), filepath.FromSlash(rel)))
	return string(data)
}

var incidents = []incident{
	{
		// Hidden stargazers: GitHub's stargazer list came back empty for
		// repositories with stars.
		name: "hidden stargazers", check: "reconcile",
		run: func(t *testing.T, checks []check.Checker) (bool, bool) {
			w := newWorld(t)
			w.cfg.Projects["proj"] = config.Project{PyPI: config.StringList{"pkg"}, GitHubEvents: config.StringList{"org/repo"}}
			ref := referenceFor{allTime: []source.ReferenceTotal{{Target: "org/repo", Kind: "records", Metric: "daily_stars", Label: "stars", Value: 3400, Tolerance: 34}}}
			out := w.gateNight(checks, false, map[string]source.Reconciler{"github": ref}, nil)
			return has(out.Night.Findings, "reconcile.totals", check.Warning), true
		},
	},
	{
		// Deleted history in a run: a full re-fetch replaced a project's
		// files without its history.
		name: "deleted history in a run", check: "diff",
		run: func(t *testing.T, checks []check.Checker) (bool, bool) {
			w := newWorld(t)
			before := fileOf(w, "metrics/pypi/proj/2026-10-19.jsonl")
			out := w.gateNight(checks, true, nil, func(context.Context, fetch.Options) ([]fetch.Result, error) {
				return nil, os.RemoveAll(store.MetricsProjectDir(w.cfg.DataDir(), "pypi", "proj"))
			})
			return has(out.Night.Findings, "diff.records-removed", check.Error), fileOf(w, "metrics/pypi/proj/2026-10-19.jsonl") == before
		},
	},
	{
		// Deleted history in a commit made by hand.
		name: "deleted history in a commit made by hand", check: "diff",
		run: func(t *testing.T, checks []check.Checker) (bool, bool) {
			w := newWorld(t)
			w.gateNight(checks, false, nil, func(context.Context, fetch.Options) ([]fetch.Result, error) { return nil, nil })
			w.byHand(func(dataDir string) { _ = os.RemoveAll(filepath.Join(dataDir, "metrics/pypi/proj")) }, "Re-fetch by hand")
			w.pull()
			out := w.gateNight(checks, false, nil, func(context.Context, fetch.Options) ([]fetch.Result, error) { return nil, nil })
			again := w.gateNight(checks, false, nil, func(context.Context, fetch.Options) ([]fetch.Result, error) { return nil, nil })
			// Kept: the finding stays open until the records are back.
			return len(out.Hand) > 0, len(again.Hand) > 0
		},
	},
	{
		// CRAN month ends: days that were never fetched.
		name: "CRAN month ends", check: "reconcile",
		run: func(t *testing.T, checks []check.Checker) (bool, bool) {
			w := newWorld(t)
			w.cfg.Projects["proj"] = config.Project{CRAN: config.StringList{"pkg"}}
			delete(w.cfg.Projects, "other")
			// September without its last day.
			var records []source.Record
			for d := 1; d < 30; d++ {
				records = append(records, source.Record{Metric: "daily_downloads", ProjectID: "proj", Target: "pkg", Date: fmt.Sprintf("2026-09-%02d", d), Value: 10})
			}
			if _, err := store.WriteRecords(w.cfg.DataDir(), "cran", "proj", records); err != nil {
				t.Fatal(err)
			}
			for len(reconcile.Tonight([]string{"pkg"}, w.day)) == 0 {
				w.day = w.day.AddDate(0, 0, 1)
			}
			ref := referenceFor{monthly: []source.ReferenceTotal{{Target: "pkg", Kind: "records", Metric: "daily_downloads", Label: "downloads", Value: 300, Tolerance: 3}}}
			out := w.gateNight(checks, false, map[string]source.Reconciler{"cran": ref}, func(context.Context, fetch.Options) ([]fetch.Result, error) { return nil, nil })
			return has(out.Night.Findings, "reconcile.totals", check.Warning), true
		},
	},
	{
		// Plausible pages overlapped, storing records twice.
		name: "Plausible pages", check: "diff",
		run: func(t *testing.T, checks []check.Checker) (bool, bool) {
			w := newWorld(t)
			out := w.gateNight(checks, true, nil, func(context.Context, fetch.Options) ([]fetch.Result, error) {
				line := `{"source":"pypi","metric":"daily_downloads","project_id":"proj","target":"pkg","date":"2026-10-20","value":5}` + "\n"
				path := filepath.Join(store.MetricsProjectDir(w.cfg.DataDir(), "pypi", "proj"), "2026-10-20.jsonl")
				return nil, os.WriteFile(path, []byte(line+line), 0o644)
			})
			_, err := os.Stat(filepath.Join(w.cfg.DataDir(), "metrics/pypi/proj/2026-10-20.jsonl"))
			return has(out.Night.Findings, "data.integrity", check.Error), os.IsNotExist(err) && contains(out.HeldBack, "pypi/proj")
		},
	},
	{
		// Replaced daily files: a write dropped the reaction counts.
		name: "replaced daily files", check: "diff",
		run: func(t *testing.T, checks []check.Checker) (bool, bool) {
			w := newWorld(t)
			if _, err := store.WriteRecords(w.cfg.DataDir(), "pypi", "proj", []source.Record{{Metric: "total_downloads", ProjectID: "proj", Target: "pkg", Date: "2026-10-19", Value: 44619}}); err != nil {
				t.Fatal(err)
			}
			sh(t, w.work, "add", "-A")
			sh(t, w.work, "commit", "-q", "-m", "Totals")
			before := fileOf(w, "metrics/pypi/proj/2026-10-19.jsonl")
			out := w.gateNight(checks, true, nil, func(context.Context, fetch.Options) ([]fetch.Result, error) {
				path := filepath.Join(store.MetricsProjectDir(w.cfg.DataDir(), "pypi", "proj"), "2026-10-19.jsonl")
				return nil, store.WriteRecordsFile(path, []source.Record{{Source: "pypi", Metric: "daily_downloads", ProjectID: "proj", Target: "pkg", Date: "2026-10-19", Value: 3}})
			})
			return has(out.Night.Findings, "diff.records-removed", check.Error), fileOf(w, "metrics/pypi/proj/2026-10-19.jsonl") == before
		},
	},
	{
		// Failed nights: jobs failed, and nobody noticed.
		name: "failed nights", check: "fetch.failed",
		run: func(t *testing.T, checks []check.Checker) (bool, bool) {
			w := newWorld(t)
			before := fileOf(w, "metrics/pypi/proj/2026-10-19.jsonl")
			out := w.gateNight(checks, false, nil, func(context.Context, fetch.Options) ([]fetch.Result, error) {
				return []fetch.Result{{Source: "github", ProjectID: "proj", Error: "comments: 422"}}, nil
			})
			opened := len(w.issues.issues) == 1 && strings.Contains(w.issues.issues[0]["body"].(string), "fetch.failed")
			return has(out.Night.Findings, "fetch.failed", check.Error) && opened, fileOf(w, "metrics/pypi/proj/2026-10-19.jsonl") == before
		},
	},
}

func TestIncidents(t *testing.T) {
	for _, inc := range incidents {
		t.Run(inc.name, func(t *testing.T) {
			caught, kept := inc.run(t, check.Default())
			if !caught {
				t.Error("not reported")
			}
			if !kept {
				t.Error("the stored data wasn't kept")
			}
		})
	}
}

// With its check disabled, each incident goes unnoticed: so the check, and
// nothing else, is what catches it.
func TestIncidentMutations(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	for _, inc := range incidents {
		t.Run(inc.name+" without "+inc.check, func(t *testing.T) {
			if caught, _ := inc.run(t, without(inc.check)); caught {
				t.Errorf("still reported with %s disabled", inc.check)
			}
		})
	}
}
