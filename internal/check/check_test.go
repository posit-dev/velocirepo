package check

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/fetch"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

var now = time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)

func daily(t *testing.T, dataDir, project, target string, from, to time.Time) {
	t.Helper()
	var records []source.Record
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		records = append(records, source.Record{Metric: "daily_downloads", ProjectID: project, Target: target, Date: d.Format("2006-01-02"), Value: 5})
	}
	if _, err := store.WriteRecords(dataDir, "pypi", project, records); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, c *Context, accepted Acceptances) Report {
	t.Helper()
	if c.Now.IsZero() {
		c.Now = now
	}
	r, err := Run(context.Background(), c, Default(), accepted)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func findingsOf(r Report, check string) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Check == check {
			out = append(out, f)
		}
	}
	return out
}

func TestFetchFailed(t *testing.T) {
	r := run(t, &Context{DataDir: t.TempDir(), Results: &fetch.RunResults{Results: []fetch.Result{
		{Source: "github", ProjectID: "positron", Error: "comments: 422"},
		{Source: "cran", ProjectID: "dplyr", Records: 1},
	}}}, nil)
	failed := findingsOf(r, "fetch.failed")
	if len(failed) != 1 || failed[0].Project != "positron" || failed[0].Severity != Error {
		t.Errorf("fetch.failed = %+v", failed)
	}
	if !r.HasErrors() {
		t.Error("a failed job isn't an error")
	}
}

// Duplicates are reported once per file, as warnings: they're in stored
// data. The gate makes them errors when they're about the night's records.
func TestIntegrityGroupsDuplicatesPerFile(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "metrics", "plausible", "dplyr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"source":"plausible","metric":"daily_pageviews","project_id":"dplyr","target":"dplyr.tidyverse.org","date":"2026-10-06","value":7,"extra":{"page":"/a"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "2026-10-06.jsonl"), []byte(line+line+line), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Projects: map[string]config.Project{"dplyr": {}}}
	r := run(t, &Context{Config: cfg, DataDir: dataDir}, nil)
	dups := findingsOf(r, "data.integrity")
	if len(dups) != 1 || dups[0].Severity != Warning || dups[0].Source != "plausible" || dups[0].Project != "dplyr" || !strings.Contains(dups[0].Message, "2 times") {
		t.Errorf("data.integrity = %+v", dups)
	}
}

// A target that usually has data and suddenly has none is reported; a quiet
// target, and one with recent data, aren't.
func TestVolume(t *testing.T) {
	dataDir := t.TempDir()
	today := now.Truncate(24 * time.Hour)
	daily(t, dataDir, "stopped", "pkg", today.AddDate(0, 0, -31), today.AddDate(0, 0, -4))
	daily(t, dataDir, "fine", "pkg", today.AddDate(0, 0, -31), today.AddDate(0, 0, -1))
	daily(t, dataDir, "quiet", "pkg", today.AddDate(0, 0, -10), today.AddDate(0, 0, -6))
	r := run(t, &Context{DataDir: dataDir}, nil)
	v := findingsOf(r, "data.volume")
	if len(v) != 1 || v[0].Project != "stopped" || v[0].Severity != Warning {
		t.Errorf("data.volume = %+v, want only the stopped target", v)
	}
}

func TestAcceptances(t *testing.T) {
	results := &fetch.RunResults{Results: []fetch.Result{{Source: "github", ProjectID: "p", Error: "boom"}}}
	id := NewFinding("fetch.failed", Error, "github", "p", "", "", "").ID

	r := run(t, &Context{DataDir: t.TempDir(), Results: results}, Acceptances{{ID: id, Reason: "known"}})
	if r.HasErrors() || !findingsOf(r, "fetch.failed")[0].Accepted {
		t.Errorf("an accepted finding still counts: %+v", r.Findings)
	}
	r = run(t, &Context{DataDir: t.TempDir(), Results: results}, Acceptances{{ID: id, Reason: "known", Expires: "2026-10-20"}})
	if !r.HasErrors() {
		t.Error("an expired acceptance still applies")
	}

	path := filepath.Join(t.TempDir(), "checks.toml")
	_ = os.WriteFile(path, []byte("[[accept]]\nid = \""+id+"\"\n"), 0o644)
	if _, err := LoadAcceptances(path); err == nil {
		t.Error("an acceptance without a reason loaded")
	}
	_ = os.WriteFile(path, []byte("[[accept]]\nid = \""+id+"\"\nreason = \"bot comments\"\nexpires = \"2027-01-01\"\n"), 0o644)
	as, err := LoadAcceptances(path)
	if err != nil || len(as) != 1 {
		t.Errorf("LoadAcceptances = %v, %v", as, err)
	}
	if as, err := LoadAcceptances(filepath.Join(t.TempDir(), "missing.toml")); err != nil || as != nil {
		t.Errorf("a missing file = %v, %v", as, err)
	}
}

func TestSummaryIsStable(t *testing.T) {
	r := Report{Findings: []Finding{
		NewFinding("fetch.failed", Error, "github", "positron", "", "", "comments: 422 | pagination"),
		NewFinding("data.volume", Warning, "pypi", "dplyr", "dplyr", "", "no data"),
	}}
	r.Findings[1].Accepted, r.Findings[1].Reason = true, "known"
	got := Summary(r)
	golden := filepath.Join("testdata", "summary.md")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		_ = os.WriteFile(golden, []byte(got), 0o644)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("summary changed:\n%s\nwant:\n%s", got, want)
	}
	if Summary(Report{}) != "No problems found.\n" {
		t.Errorf("empty summary = %q", Summary(Report{}))
	}
}

func TestFreshness(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{Projects: map[string]config.Project{"proj": {PyPI: config.StringList{"fresh", "stale"}}}}
	for target, date := range map[string]string{"fresh": "2026-10-19", "stale": "2026-10-01"} {
		if err := store.MarkFetchedSeries(dataDir, "pypi", "proj", "", date, []string{target}); err != nil {
			t.Fatal(err)
		}
	}
	got := findingsOf(run(t, &Context{Config: cfg, DataDir: dataDir}, nil), "data.freshness")
	if len(got) != 1 || got[0].Target != "stale" || got[0].Severity != Warning {
		t.Errorf("data.freshness = %+v, want only the stale target", got)
	}
}
