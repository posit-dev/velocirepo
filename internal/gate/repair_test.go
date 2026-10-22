package gate

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/archive"
	"github.com/posit-dev/velocirepo/internal/check"
	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/fetch"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

func TestRepairMethods(t *testing.T) {
	now := time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)
	recent, old := now.AddDate(0, 0, -5), now.AddDate(0, -9, 0)
	for _, c := range []struct {
		source          string
		from            time.Time
		refetch, replay bool
	}{
		{"github", old, true, true},
		{"plausible", old, true, false},
		{"github-traffic", recent, true, false},
		{"github-traffic", old, false, true},
		{"pypi", old, false, true},
		{"youtube", recent, false, true},
	} {
		refetch, replay := repairMethods(c.source, c.from, now)
		if refetch != c.refetch || replay != c.replay {
			t.Errorf("%s from %s: refetch %v, replay %v", c.source, c.from.Format("2006-01-02"), refetch, replay)
		}
	}
}

// The Plausible pagination incident: stored values were wrong over a
// range. A repair re-fetches it; its changes are reported as diff.repaired
// and kept, also when enforcing, and committed with the trailer.
func TestRepairByRefetch(t *testing.T) {
	w := newWorld(t)
	w.cfg.Settings.Gate = "enforce"
	w.write("proj", "pkg", "2026-10-18", 3)
	sh(t, w.work, "add", "-A")
	sh(t, w.work, "commit", "-q", "-m", "More days")
	rep, _ := check.ParseRepair("pypi/proj 2026-10-18..2026-10-19")
	day := w.day
	var asked fetch.Options
	out, err := Run(context.Background(), Options{Config: w.cfg, GitEnv: gitEnv, Now: func() time.Time { return day }, Repair: &rep,
		Fetch: func(ctx context.Context, o fetch.Options) ([]fetch.Result, error) {
			asked = o
			var records []source.Record
			for _, d := range []string{"2026-10-18", "2026-10-19"} {
				records = append(records, source.Record{Metric: "daily_downloads", ProjectID: "proj", Target: "pkg", Date: d, Value: 1})
			}
			_, err := store.WriteRecordsScoped(w.cfg.DataDir(), "pypi", "proj", records, store.RecordScope{Windows: map[string]source.Window{"pkg": {Start: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)}}})
			return []fetch.Result{{Source: "pypi", ProjectID: "proj", Records: 2}}, err
		}})
	if err != nil {
		t.Fatal(err)
	}
	if !asked.Repair || asked.StartDate != "2026-10-18" || asked.EndDate != "2026-10-19" || asked.Project != "proj" || strings.Join(asked.Sources, ",") != "pypi" {
		t.Errorf("fetch options = %+v", asked)
	}
	repaired := 0
	for _, f := range out.Night.Findings {
		if f.Check == "diff.repaired" {
			repaired++
		}
		if f.Severity == check.Error {
			t.Errorf("error: %+v", f)
		}
	}
	if repaired == 0 {
		t.Errorf("findings = %+v, want diff.repaired", out.Night.Findings)
	}
	records, _ := store.ReadRecords(filepath.Join(w.cfg.DataDir(), "metrics/pypi/proj/2026-10-19.jsonl"))
	if len(records) != 1 || records[0].Value != 1 {
		t.Errorf("the repair was undone: %+v", records)
	}
	msg := sh(t, w.work, "log", "-1", "--format=%B")
	if !strings.HasPrefix(msg, "Repair pypi/proj 2026-10-18..2026-10-19") || !strings.Contains(msg, "Velocirepo-Repair: pypi/proj 2026-10-18..2026-10-19") {
		t.Errorf("message = %q", msg)
	}
	// The gate doesn't check its own repair commit again.
	if remoteHead(t, w, "main") != remoteHead(t, w, checkedRef) {
		t.Error("the checked ref isn't past the repair")
	}
}

// A snapshot source is repaired by replaying the archive, never by
// re-fetching: a processing bug stored a wrong value, and the replay writes
// the one the archived response gives.
func TestRepairByReplay(t *testing.T) {
	w := newWorld(t)
	p := w.cfg.Projects["proj"]
	p.Homebrew = config.StringList{"pkg"}
	w.cfg.Projects["proj"] = p
	if _, err := store.WriteRecords(w.cfg.DataDir(), "homebrew", "proj", []source.Record{{Metric: "downloads_30d", ProjectID: "proj", Target: "pkg", Date: "2026-10-19", Value: 999}}); err != nil {
		t.Fatal(err)
	}
	sh(t, w.work, "add", "-A")
	sh(t, w.work, "commit", "-q", "-m", "A buggy night")

	arch := t.TempDir()
	runTime := time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC)
	aw, err := archive.NewWriter(arch, "20261020T093000Z-1-1", "test", runTime)
	if err != nil {
		t.Fatal(err)
	}
	_ = aw.Write(archive.Exchange{Source: "homebrew", Project: "proj", Target: "pkg", Method: "GET", URL: "https://formulae.brew.sh/api/formula/pkg.json", Status: 200,
		Body: `{"name":"pkg","full_name":"pkg","analytics":{"install":{"30d":{"pkg":10},"90d":{"pkg":20},"365d":{"pkg":30}}}}`})
	aw.AddJob(archive.Job{Source: "homebrew", Project: "proj", Target: "pkg", Start: map[string]string{"": "2026-10-19"}, End: map[string]string{"": "2026-10-19"}})
	if _, err := aw.Close(); err != nil {
		t.Fatal(err)
	}

	rep, _ := check.ParseRepair("homebrew/proj 2026-10-19..2026-10-19")
	day := w.day
	refetched := false
	_, err = Run(context.Background(), Options{Config: w.cfg, GitEnv: gitEnv, Now: func() time.Time { return day }, Repair: &rep,
		Fetch: func(context.Context, fetch.Options) ([]fetch.Result, error) { refetched = true; return nil, nil },
		Runs: func(ctx context.Context, r check.Repair, dir string) ([]string, error) {
			return archive.RunsInRange(arch, r.Source, r.Project, r.From, r.To)
		}})
	if err != nil {
		t.Fatal(err)
	}
	if refetched {
		t.Error("a snapshot source was re-fetched for a past range")
	}
	paths, _ := filepath.Glob(filepath.Join(store.MetricsProjectDir(w.cfg.DataDir(), "homebrew", "proj"), "*.jsonl"))
	var got []source.Record
	for _, p := range paths {
		rs, _ := store.ReadRecords(p)
		got = append(got, rs...)
	}
	found := false
	for _, r := range got {
		if r.Date == "2026-10-19" && r.Metric == "downloads_30d" {
			found = true
			if r.Value != 10 {
				t.Errorf("%s = %d, want the archived 10", r.Metric, r.Value)
			}
		}
	}
	if !found {
		t.Errorf("records = %+v", got)
	}
}
