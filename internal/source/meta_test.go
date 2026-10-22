package source

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

// Every source describes what it writes, so the data dictionary documents
// every metric, event type and column.
func TestEverySourceDescribesItsData(t *testing.T) {
	base := map[string]bool{
		"project": true, "source": true, "target": true, "metric": true, "date": true, "value": true, "extra": true,
		"indicator": true, "id": true, "ref": false, "title": true, "description": true, "content": true,
		"published_at": true, "updated_at": true, "url": true, "duration": true, "tags": true, "type": true,
	}
	columnTypes := map[string]string{}
	measures := map[string]string{}
	for _, d := range sourceinfo.All() {
		m, _ := MetaFor(d.Name)
		if m.Target == "" {
			t.Errorf("%s doesn't say what a target is", d.Name)
		}
		if len(m.Metrics) == 0 {
			t.Errorf("%s declares no metrics", d.Name)
		}
		columns := map[string]bool{}
		for _, c := range m.Columns {
			if c.Table != "metrics" && c.Table != "content" {
				t.Errorf("%s column %s is for table %q; use metrics or content", d.Name, c.Key, c.Table)
			}
			if c.Type != "string" && c.Type != "integer" {
				t.Errorf("%s column %s has type %q; use string or integer", d.Name, c.Key, c.Type)
			}
			if base[c.Key] {
				t.Errorf("%s column %s clashes with a column every table has", d.Name, c.Key)
			}
			if c.Description == "" {
				t.Errorf("%s column %s has no description", d.Name, c.Key)
			}
			if prev, ok := columnTypes[c.Table+"."+c.Key]; ok && prev != c.Type {
				t.Errorf("column %s.%s is %s in one source and %s in %s", c.Table, c.Key, prev, c.Type, d.Name)
			}
			columnTypes[c.Table+"."+c.Key] = c.Type
			if c.Table == "metrics" {
				columns[c.Key] = true
			}
		}
		for _, metric := range m.Metrics {
			if metric.Kind == MetricUndeclared {
				t.Errorf("%s metric %s declares no kind", d.Name, metric.Name)
			}
			if metric.Description == "" {
				t.Errorf("%s metric %s has no description", d.Name, metric.Name)
			}
			if metric.Key != "" && !columns[metric.Key] {
				t.Errorf("%s metric %s is keyed by %s, which isn't one of its metrics columns", d.Name, metric.Name, metric.Key)
			}
			if metric.Measure != "" {
				if metric.Kind != MetricDaily {
					t.Errorf("%s metric %s has a measure but isn't daily", d.Name, metric.Name)
				}
				if metric.MeasureDescription == "" {
					t.Errorf("%s measure %s has no description", d.Name, metric.Measure)
				}
				if prev, ok := measures[metric.Measure]; ok {
					t.Errorf("measure %s is declared by %s and %s", metric.Measure, prev, d.Name)
				}
				measures[metric.Measure] = d.Name
			}
		}
		for _, e := range m.Events {
			if e.Name == "" || e.Description == "" {
				t.Errorf("%s declares an event type without a name or description: %+v", d.Name, e)
			}
		}
		for _, c := range m.Content {
			if c.Description == "" {
				t.Errorf("%s content type %q has no description", d.Name, c.Name)
			}
		}
		if (d.Category == sourceinfo.CategoryEvents) != (len(m.Events) > 0) {
			t.Errorf("%s is in category %s but declares %d event types", d.Name, d.Category, len(m.Events))
		}
	}
}

// Every metric name written in a source's code is declared, so a new metric
// can't go undocumented. Names built at run time, like Homebrew's
// downloads_<period>, are declared by hand.
func TestWrittenMetricsAreDeclared(t *testing.T) {
	declared := map[string]bool{}
	for _, m := range Metas() {
		for _, metric := range m.Metrics {
			declared[metric.Name] = true
		}
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	name := regexp.MustCompile(`^(daily|total)_[a-z_]+$`)
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		// Keys of map literals are API fields, such as Plausible's
		// "total_rows", not metrics.
		keys := map[ast.Node]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			if kv, ok := n.(*ast.KeyValueExpr); ok {
				keys[kv.Key] = true
			}
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || keys[lit] {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err == nil && name.MatchString(s) && !declared[s] {
				t.Errorf("%s writes metric %s, which no source declares", fset.Position(lit.Pos()), s)
			}
			return true
		})
	}
}
