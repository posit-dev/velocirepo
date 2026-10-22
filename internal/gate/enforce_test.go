package gate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/check"
	"github.com/posit-dev/velocirepo/internal/fetch"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

// resumingFetch stores every day after each target's watermark through
// yesterday, as the real planner would, and records the days it fetched.
type resumingFetch struct {
	w *world
	// broken makes a project's new records duplicated, an error about them.
	broken map[string]bool
	// totals gives a project's total_downloads for the night, if set.
	totals map[string]int64
	asked  []string
}

func (f *resumingFetch) run(day time.Time) func(context.Context, fetch.Options) ([]fetch.Result, error) {
	return func(ctx context.Context, o fetch.Options) ([]fetch.Result, error) {
		dataDir := f.w.cfg.DataDir()
		yesterday := day.AddDate(0, 0, -1)
		for project, p := range f.w.cfg.Projects {
			target := p.PyPI[0]
			marks, _ := store.SeriesWatermarks(dataDir, "pypi", project, "")
			start := yesterday
			if m := marks[target]; m != "" {
				t, _ := time.Parse("2006-01-02", m)
				start = t.AddDate(0, 0, 1)
			}
			for d := start; !d.After(yesterday); d = d.AddDate(0, 0, 1) {
				date := d.Format("2006-01-02")
				f.asked = append(f.asked, project+" "+date)
				records := []source.Record{{Metric: "daily_downloads", ProjectID: project, Target: target, Date: date, Value: 5}}
				if v, ok := f.totals[project]; ok {
					records = append(records, source.Record{Metric: "total_downloads", ProjectID: project, Target: target, Date: date, Value: v})
				}
				if _, err := store.WriteRecords(dataDir, "pypi", project, records); err != nil {
					return nil, err
				}
				if f.broken[project] {
					path := filepath.Join(store.MetricsProjectDir(dataDir, "pypi", project), date+".jsonl")
					data, _ := os.ReadFile(path)
					_ = os.WriteFile(path, append(data, data...), 0o644)
				}
			}
			_ = store.MarkFetchedSeries(dataDir, "pypi", project, "", yesterday.Format("2006-01-02"), []string{target})
		}
		return []fetch.Result{{Source: "pypi", ProjectID: "proj", Records: 1}}, nil
	}
}

func (w *world) enforcingNight(f *resumingFetch, opts Options) Outcome {
	w.t.Helper()
	day := w.day
	w.day = w.day.AddDate(0, 0, 1)
	w.cfg.Settings.Gate = "enforce"
	opts.Config, opts.GitEnv = w.cfg, gitEnv
	opts.Now = func() time.Time { return day }
	opts.Fetch = f.run(day)
	opts.NoConcatenate = true
	out, err := Run(context.Background(), opts)
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

func storedDates(t *testing.T, w *world, project string) []string {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(store.MetricsProjectDir(w.cfg.DataDir(), "pypi", project), "*.jsonl"))
	var out []string
	for _, p := range paths {
		records, err := store.ReadRecords(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range records {
			if r.Metric == "daily_downloads" {
				out = append(out, r.Date)
			}
		}
	}
	return out
}

func storedTotal(t *testing.T, w *world, project string) (string, int64) {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(store.MetricsProjectDir(w.cfg.DataDir(), "pypi", project), "*.jsonl"))
	date, value := "", int64(0)
	for _, p := range paths {
		records, _ := store.ReadRecords(p)
		for _, r := range records {
			if r.Metric == "total_downloads" && r.Date > date {
				date, value = r.Date, r.Value
			}
		}
	}
	return date, value
}

// A night that removes records or changes values leaves the stored data as
// it was, and reports both.
func TestEnforceRestoresRemovalsAndValues(t *testing.T) {
	w := newWorld(t)
	f := &resumingFetch{w: w}
	w.enforcingNight(f, Options{})
	dataDir := w.cfg.DataDir()
	before, _ := os.ReadFile(filepath.Join(dataDir, "metrics/pypi/proj/2026-10-19.jsonl"))

	// A buggy night: deletes a stored file and changes a stored value.
	f2 := &resumingFetch{w: w}
	run := f2.run(w.day)
	day := w.day
	w.day = w.day.AddDate(0, 0, 1)
	out, err := Run(context.Background(), Options{Config: w.cfg, GitEnv: gitEnv, NoConcatenate: true, Now: func() time.Time { return day },
		Fetch: func(ctx context.Context, o fetch.Options) ([]fetch.Result, error) {
			res, err := run(ctx, o)
			_ = os.Remove(filepath.Join(dataDir, "metrics/pypi/proj/2026-10-19.jsonl"))
			_, _ = store.WriteRecords(dataDir, "pypi", "other", []source.Record{{Metric: "daily_downloads", ProjectID: "other", Target: "lib", Date: "2026-10-19", Value: 99}})
			return res, err
		}})
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dataDir, "metrics/pypi/proj/2026-10-19.jsonl"))
	if err != nil || string(after) != string(before) {
		t.Errorf("the removed records weren't restored: %q, %v", after, err)
	}
	records, _ := store.ReadRecords(filepath.Join(dataDir, "metrics/pypi/other/2026-10-19.jsonl"))
	if len(records) != 1 || records[0].Value != 3 {
		t.Errorf("the changed value wasn't kept: %+v", records)
	}
	if strings.Join(out.WouldRestore, ",") != "pypi/proj" || strings.Join(out.WouldKeep, ",") != "pypi/other" {
		t.Errorf("restored %v, kept %v", out.WouldRestore, out.WouldKeep)
	}
	if len(out.HeldBack) != 0 {
		t.Errorf("held back %v; removals and changes don't hold back new records", out.HeldBack)
	}
	dates := storedDates(t, w, "proj")
	if dates[len(dates)-1] != day.AddDate(0, 0, -1).Format("2006-01-02") {
		t.Errorf("the night's new records didn't land next to the restored ones: %v", dates)
	}
}

// An error about one project's new records holds back only that project,
// for as long as it lasts; the fetch continues from the held-back data, and
// once the error is gone, all of it lands, including a total that changed
// and changed back.
func TestEnforceHoldsBackPerProject(t *testing.T) {
	w := newWorld(t)
	f := &resumingFetch{w: w, broken: map[string]bool{"proj": true}, totals: map[string]int64{"proj": 105}}
	out := w.enforcingNight(f, Options{})
	if strings.Join(out.HeldBack, ",") != "pypi/proj" {
		t.Fatalf("held back %v, want only pypi/proj", out.HeldBack)
	}
	if contains(storedDates(t, w, "proj"), "2026-10-20") || !contains(storedDates(t, w, "other"), "2026-10-20") {
		t.Errorf("proj %v, other %v: want proj held back and other landed", storedDates(t, w, "proj"), storedDates(t, w, "other"))
	}
	if marks, _ := store.SeriesWatermarks(w.cfg.DataDir(), "pypi", "proj", ""); marks["pkg"] != "2026-10-19" {
		t.Errorf("the stored watermark moved past held-back data: %v", marks)
	}
	if msg := sh(t, w.work, "log", "-1", "--format=%B"); !strings.Contains(msg, "Held back: pypi/proj (data.integrity), see velocirepo/held-back/pypi/proj/") {
		t.Errorf("message = %q", msg)
	}

	// The second night continues from the held-back data, and is held back
	// with it.
	f.asked = nil
	f.totals["proj"] = 100
	out = w.enforcingNight(f, Options{})
	if strings.Join(f.asked, ",") != "other 2026-10-21,proj 2026-10-21" && strings.Join(f.asked, ",") != "proj 2026-10-21,other 2026-10-21" {
		t.Errorf("the second night fetched %v, want only 2026-10-21", f.asked)
	}
	if strings.Join(out.HeldBack, ",") != "pypi/proj" {
		t.Fatalf("second night held back %v", out.HeldBack)
	}

	// Fixed: everything lands.
	f.broken = nil
	f.asked = nil
	out = w.enforcingNight(f, Options{})
	if len(out.HeldBack) != 0 {
		t.Fatalf("still held back: %v", out.HeldBack)
	}
	for _, d := range []string{"2026-10-20", "2026-10-21", "2026-10-22"} {
		if !contains(storedDates(t, w, "proj"), d) {
			t.Errorf("%s didn't land: %v", d, storedDates(t, w, "proj"))
		}
	}
	if date, v := storedTotal(t, w, "proj"); v != 100 {
		t.Errorf("total = %d on %s, want 100", v, date)
	}
	if _, err := os.Stat(filepath.Join(w.work, "velocirepo", HeldBackDir, "pypi", "proj")); !os.IsNotExist(err) {
		t.Errorf("the held-back directory is still there: %v", err)
	}
}

func TestEnforceReleaseAndDiscard(t *testing.T) {
	w := newWorld(t)
	f := &resumingFetch{w: w, broken: map[string]bool{"proj": true}}
	w.enforcingNight(f, Options{})

	// Discard: the held-back days are fetched again.
	f.asked = nil
	out := w.enforcingNight(f, Options{Discard: "pypi/proj"})
	if !contains(f.asked, "proj 2026-10-20") {
		t.Errorf("after discard, fetched %v; want the discarded day again", f.asked)
	}
	if !strings.Contains(strings.Join(out.Changes.Notes, " "), "Discarded") {
		t.Errorf("notes = %v", out.Changes.Notes)
	}

	// Release: lands regardless of the findings.
	out = w.enforcingNight(f, Options{Release: "pypi/proj"})
	if len(out.HeldBack) != 0 || !contains(storedDates(t, w, "proj"), "2026-10-21") {
		t.Errorf("held back %v, stored %v; want released", out.HeldBack, storedDates(t, w, "proj"))
	}
}

// Without [settings] gate = "enforce", nothing is held back.
func TestReportOnlyHoldsNothingBack(t *testing.T) {
	w := newWorld(t)
	f := &resumingFetch{w: w, broken: map[string]bool{"proj": true}}
	day := w.day
	out, err := Run(context.Background(), Options{Config: w.cfg, GitEnv: gitEnv, NoConcatenate: true, Now: func() time.Time { return day }, Fetch: f.run(day)})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.HeldBack) != 0 || strings.Join(out.WouldHoldBack, ",") != "pypi/proj" || !contains(storedDates(t, w, "proj"), "2026-10-20") {
		t.Errorf("held back %v, would %v", out.HeldBack, out.WouldHoldBack)
	}
}

func TestBlocking(t *testing.T) {
	keys, global := blocked([]check.Finding{
		check.NewFinding("data.integrity", check.Error, "github", "positron", "", "dup", "dup"),
		check.NewFinding("diff.records-removed", check.Error, "pypi", "proj", "", "", "removed"),
		check.NewFinding("fetch.failed", check.Error, "cran", "dplyr", "", "", "502"),
		check.NewFinding("data.volume", check.Warning, "plausible", "quarto", "", "", "quiet"),
		check.NewFinding("config", check.Warning, "", "", "", "", "a global warning"),
	})
	if len(keys) != 1 || !keys["github/positron"] || global {
		t.Errorf("blocked %v, global %v", keys, global)
	}
	if _, global := blocked([]check.Finding{check.NewFinding("config", check.Error, "", "", "", "", "unreadable")}); !global {
		t.Error("a global error doesn't hold back everything")
	}
}
