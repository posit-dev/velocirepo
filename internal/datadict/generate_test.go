package datadict

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
	"github.com/posit-dev/velocirepo/internal/testutil"
)

var updateGolden = flag.Bool("update", false, "update the golden files")

const fixtureConfig = `
[data]
dir = "data"

[projects.shiny]
name = "Shiny"
description = "Web apps in R"
tags = ["r"]
github = "rstudio/shiny"
cran = "shiny"
youtube = "@shiny"
notes = "The Shiny package for R."

[projects.shiny-old]
github = "rstudio/shiny"
duplicate_of = "shiny"

[projects.site]
name = "Site"
plausible = "example.com"
rss = "https://example.com/blog/index.xml"

[data_dict]
name = "example"
label = "Example metrics"
description = "Adoption metrics of an example portfolio."
details = "Collected nightly."

[data_dict.values.content.type]
post = "a blog post on example.com"

[[data_dict.notes]]
tables = ["metrics"]
source = "cran"
text = "2026-01-02 is missing."

[[data_dict.notes]]
tables = ["events"]
column = "user"
text = "Bots are included."

[[data_dict.definitions]]
table = "metrics"
name = "large"
label = "100 or more"
description = "Rows with a value of 100 or more."
expr = "value >= 100"

[data_dict.glossary]
"portfolio" = "The projects of the example organization."
`

func intPtr(n int) *int       { return &n }
func int64Ptr(n int64) *int64 { return &n }

// fixture writes a config and a little of every kind of data, and returns
// the materialized export.
func fixture(t *testing.T, format string) (*config.Config, *store.Exported) {
	t.Helper()
	cfg, err := config.Load(testutil.WriteConfig(t, fixtureConfig))
	if err != nil {
		t.Fatal(err)
	}
	dataDir := cfg.DataDir()
	write := func(src, project string, records []source.Record) {
		if _, err := store.WriteRecords(dataDir, src, project, records); err != nil {
			t.Fatal(err)
		}
	}
	write("cran", "shiny", []source.Record{
		{Metric: "daily_downloads", ProjectID: "shiny", Target: "shiny", Date: "2026-01-01", Value: 100},
		{Metric: "daily_downloads", ProjectID: "shiny", Target: "shiny", Date: "2026-01-03", Value: 120},
	})
	write("youtube", "shiny", []source.Record{
		{Metric: "total_subscribers", ProjectID: "shiny", Target: "@shiny", Date: "2026-01-01", Value: 1000},
		{Metric: "total_views", ProjectID: "shiny", Target: "@shiny", Date: "2026-01-01", Value: 50, Extra: map[string]string{"video_id": "abc123def45"}},
	})
	write("github", "shiny", []source.Record{
		{Metric: "total_reactions", ProjectID: "shiny", Target: "rstudio/shiny", Date: "2026-01-02", Value: 3, Extra: map[string]string{"ref": "7"}},
	})
	write("plausible", "site", []source.Record{
		{Metric: "daily_site_pageviews", ProjectID: "site", Target: "example.com", Date: "2026-01-01", Value: 10},
		{Metric: "daily_pageviews", ProjectID: "site", Target: "example.com", Date: "2026-01-01", Value: 10, Extra: map[string]string{"page": "/"}},
	})
	write("rss", "site", []source.Record{
		{Metric: "total_items", ProjectID: "site", Target: "https://example.com/blog/index.xml", Date: "2026-01-03", Value: 2},
	})
	for _, project := range []string{"shiny", "shiny-old"} {
		if _, err := store.WriteEvents(dataDir, "github", project, []source.Event{
			{Type: "issue_open", ProjectID: project, Target: "rstudio/shiny", Datetime: "2026-01-01T09:00:00Z", Ref: intPtr(7), User: "alice"},
			{Type: "comment", ProjectID: project, Target: "rstudio/shiny", Datetime: "2026-01-02T10:30:00Z", Ref: intPtr(7), User: "bob"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	content := []struct {
		src, project, file string
		entries            []source.ContentEntry
	}{
		{"github", "shiny", "issues.jsonl", []source.ContentEntry{{Source: "github", Target: "rstudio/shiny", ID: "issue/7", Ref: intPtr(7), Type: "issue",
			Title: "A bug", Description: "It breaks.", PublishedAt: "2026-01-01T09:00:00Z", URL: "https://github.com/rstudio/shiny/issues/7",
			Tags: []string{"bug"}, Extra: map[string]any{"author": "alice", "state": "OPEN"}}}},
		{"youtube", "shiny", "videos.jsonl", []source.ContentEntry{{Source: "youtube", Target: "@shiny", ID: "abc123def45", Type: "video",
			Title: "Intro", PublishedAt: "2025-12-01T12:00:00Z", URL: "https://youtube.com/watch?v=abc123def45", Duration: int64Ptr(300)}}},
		{"rss", "site", "blog.jsonl", []source.ContentEntry{{Source: "rss", Target: "https://example.com/blog/index.xml", ID: "https://example.com/blog/hello", Type: "post",
			Title: "Hello", Content: "First post.", PublishedAt: "2026-01-03T00:00:00Z", URL: "https://example.com/blog/hello"}}},
	}
	for _, c := range content {
		if err := store.WriteContent(dataDir, c.src, c.project, c.file, c.entries); err != nil {
			t.Fatal(err)
		}
	}

	x, err := store.Materialize(store.ExportOptions{
		DataDir: dataDir, Format: format, Projects: cfg.ProjectInfos(), Indicators: cfg.IndicatorDefs(),
		BuiltAt: time.Date(2026, 1, 4, 6, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = x.Close() })
	return cfg, x
}

func generate(t *testing.T, cfg *config.Config, x *store.Exported) []byte {
	t.Helper()
	d, warnings, err := Generate(x, Options{Config: cfg, Indicators: cfg.IndicatorDefs()})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) > 0 {
		t.Errorf("warnings: %v", warnings)
	}
	data, err := d.YAML()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGenerateGolden(t *testing.T) {
	cfg, x := fixture(t, "duckdb")
	got := generate(t, cfg, x)
	golden := filepath.Join("testdata", "data-dict.yaml")
	if *updateGolden {
		if err := os.WriteFile(golden, got, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the dictionary differs from %s; run go test ./internal/datadict -run Golden -update", golden)
	}
}

func TestGenerateDescribesTheConfig(t *testing.T) {
	cfg, x := fixture(t, "duckdb")
	got := string(generate(t, cfg, x))
	for _, want := range []string{
		"name: example",
		"label: Example metrics",
		"Adoption metrics of an example portfolio.",
		"CRAN: 2026-01-02 is missing.",
		"Bots are included.",
		"name: large",
		"portfolio: The projects of the example organization.",
		"shiny: The Shiny package for R.",
		"post: a blog post on example.com",
		"project NOT IN ('shiny-old')",
		"AND deduplicated THEN value",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the dictionary lacks %q", want)
		}
	}
}

func TestGenerateRejectsWhatTheExportLacks(t *testing.T) {
	cfg, x := fixture(t, "duckdb")
	cfg.DataDict.Notes = append(cfg.DataDict.Notes, config.DataDictNote{Tables: []string{"metrics"}, Column: "nope", Text: "x"})
	if _, _, err := Generate(x, Options{Config: cfg}); err == nil || !strings.Contains(err.Error(), `no column "nope"`) {
		t.Errorf("err = %v", err)
	}

	cfg, x = fixture(t, "duckdb")
	cfg.DataDict.Definitions = append(cfg.DataDict.Definitions, config.DataDictDefinition{Table: "metrics", Name: "value", Expr: "1"})
	if _, _, err := Generate(x, Options{Config: cfg}); err == nil || !strings.Contains(err.Error(), "name of a column") {
		t.Errorf("err = %v", err)
	}
}

// A definition of the config replaces the one velocirepo writes.
func TestConfigDefinitionsReplaceGeneratedOnes(t *testing.T) {
	cfg, x := fixture(t, "duckdb")
	cfg.DataDict.Definitions = append(cfg.DataDict.Definitions, config.DataDictDefinition{
		Table: "metrics", Name: "pageviews", Description: "Both eras.", Expr: "SUM(value)",
	})
	d, _, err := Generate(x, Options{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, table := range d.Tables {
		for _, def := range table.Definitions {
			if table.Name == "metrics" && def.Name == "pageviews" {
				n++
				if def.Expr != "SUM(value)" || def.Description != "Both eras." {
					t.Errorf("pageviews = %+v", def)
				}
			}
		}
	}
	if n != 1 {
		t.Errorf("metrics has %d pageviews definitions, want 1", n)
	}
}

// What the config leaves open is a todo in the dictionary and a warning.
func TestGenerateWarnsAboutWhatIsOpen(t *testing.T) {
	cfg, x := fixture(t, "duckdb")
	cfg.DataDict.Values = nil
	p := cfg.Projects["shiny-old"]
	p.DuplicateOf = ""
	cfg.Projects["shiny-old"] = p
	d, warnings, err := Generate(x, Options{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 2 {
		t.Errorf("warnings = %q, want one about post, one about rstudio/shiny", warnings)
	}
	data, _ := d.YAML()
	if n := strings.Count(string(data), "todo:"); n < 2 {
		t.Errorf("the dictionary has %d todos", n)
	}
}

// The dictionary describes every column of every table of a duckdb export.
func TestGenerateMatchesTheDatabase(t *testing.T) {
	cfg, x := fixture(t, "duckdb")
	dir := t.TempDir()
	written, err := x.Write(dir, "duckdb")
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := Generate(x, Options{Config: cfg, Indicators: cfg.IndicatorDefs()})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("duckdb", written[0]+"?access_mode=read_only")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, table := range d.Tables {
		rows, err := db.Query("SELECT column_name FROM duckdb_columns() WHERE table_name = ? ORDER BY column_index", table.Name)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				t.Fatal(err)
			}
			got = append(got, c)
		}
		_ = rows.Close()
		var want []string
		for _, c := range table.Columns {
			want = append(want, c.Name)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: database columns %v, dictionary columns %v", table.Name, got, want)
		}
	}
}

// data-dict itself validates the dictionary. It checks data only in
// parquet files, so the values are checked by the tests above, against the
// database. Skipped without the data-dict CLI; CI installs it.
func TestDataDictValidates(t *testing.T) {
	if _, err := exec.LookPath("data-dict"); err != nil {
		t.Skip("data-dict isn't installed (uv tool install data-dict-yaml)")
	}
	cfg, x := fixture(t, "duckdb")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), generate(t, cfg, x), 0644); err != nil {
		t.Fatal(err)
	}
	// Warnings, such as todos, exit 0, so the report's status is what
	// counts.
	if problems := validate(t, "validate-spec", dir); len(problems) > 0 {
		t.Errorf("validate-spec: %s", strings.Join(problems, "\n"))
	}
}

// validate runs a data-dict command and returns its problems.
func validate(t *testing.T, command, dir string) []string {
	t.Helper()
	out, err := exec.Command("data-dict", command, dir, "--json").Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("data-dict %s: %v", command, err)
		}
	}
	var report struct {
		Status   string `json:"status"`
		Problems []struct {
			Code     string   `json:"code"`
			Severity string   `json:"severity"`
			Message  string   `json:"message"`
			Table    string   `json:"table"`
			Columns  []string `json:"columns"`
		} `json:"problems"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("data-dict %s: %v\n%s", command, err, out)
	}
	var problems []string
	for _, p := range report.Problems {
		where := p.Table
		if len(p.Columns) > 0 {
			where += "." + strings.Join(p.Columns, ",")
		}
		problems = append(problems, strings.TrimSpace(p.Severity+" "+p.Code+" "+where+" "+p.Message))
	}
	if report.Status != "ok" && len(problems) == 0 {
		problems = append(problems, "status "+report.Status)
	}
	return problems
}
