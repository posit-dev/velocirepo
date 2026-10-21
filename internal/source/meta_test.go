package source

import (
	"testing"

	"github.com/posit-dev/velocirepo/internal/sourceinfo"
)

// Every source declares its metadata, like the test that keeps badge presets
// in line with the metrics sources emit: a new source can't end up archived,
// unchecked or unreconciled by accident.
func TestEverySourceDeclaresItsMetadata(t *testing.T) {
	for _, d := range sourceinfo.All() {
		m, ok := MetaFor(d.Name)
		if !ok {
			t.Errorf("source %q registers no metadata", d.Name)
			continue
		}
		if m.Archive == ArchiveUndeclared {
			t.Errorf("source %q declares no archive mode", d.Name)
		}
		if m.Archive == ArchiveReadFields && m.Reduce == nil {
			t.Errorf("source %q archives read fields only but has no Reduce", d.Name)
		}
		if _, ok := m.Freshness[""]; !ok {
			t.Errorf("source %q declares no freshness limit", d.Name)
		}
		if !m.Reconciles && m.NoReferenceTotals == "" {
			t.Errorf("source %q neither provides reference totals nor says why not", d.Name)
		}
	}
}

// A source that declares reference totals implements them, and one that
// implements them declares them.
func TestReconcilersAreDeclared(t *testing.T) {
	sources := map[string]any{
		"github": &GitHubEvents{}, "github-traffic": &GitHubTraffic{}, "pypi": &PyPI{}, "cran": &CRAN{}, "homebrew": &Homebrew{},
		"plausible": &Plausible{}, "openvsx": &OpenVSX{}, "youtube": &YouTube{}, "linkedin": &LinkedIn{}, "rss": &RSS{},
	}
	for name, src := range sources {
		m, _ := MetaFor(name)
		_, implements := src.(Reconciler)
		if m.Reconciles != implements {
			t.Errorf("%s declares Reconciles = %v, but implementing Reconciler is %v", name, m.Reconciles, implements)
		}
	}
}

func TestFreshnessLimitFallsBackToTheMainSeries(t *testing.T) {
	if limit, ok := FreshnessLimit("github", "stars"); !ok || limit != 2 {
		t.Errorf("github stars limit = %d, %v; want 2", limit, ok)
	}
	if limit, ok := FreshnessLimit("cran", "anything"); !ok || limit != 3 {
		t.Errorf("cran limit = %d, %v; want 3", limit, ok)
	}
}
