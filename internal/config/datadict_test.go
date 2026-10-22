package config

import (
	"os"
	"strings"
	"testing"

	"github.com/posit-dev/velocirepo/internal/testutil"
)

const sharedTargetsConfig = `
[projects.shiny]
github = "rstudio/shiny"
duplicate_of = "shiny-r"

[projects.shiny-r]
github = "rstudio/shiny"
openvsx = "posit/shiny"
plausible = "shiny.posit.co"
notes = """
Shiny for R.
[not a section]
"""

[projects.shiny-python]
github = "posit-dev/py-shiny"
openvsx = "posit/shiny"
plausible = "shiny.posit.co"

[data_dict]
name = "example"
description = "Example metrics."

[data_dict.keep.openvsx]
"posit/shiny" = "shiny-python"

[[data_dict.notes]]
tables = ["metrics"]
source = "cran"
text = "Missing days."

[[data_dict.definitions]]
table = "metrics"
name = "recent"
expr = "date >= '2026-01-01'"

[data_dict.glossary]
"seam" = "When collection changed."
`

func TestDataDictConfigLoads(t *testing.T) {
	cfg, err := Load(testutil.WriteConfig(t, sharedTargetsConfig))
	if err != nil {
		t.Fatal(err)
	}
	dd := cfg.DataDict
	if dd.Name != "example" || len(dd.Notes) != 1 || dd.Notes[0].Source != "cran" || len(dd.Definitions) != 1 || dd.Glossary["seam"] == "" {
		t.Errorf("data_dict = %+v", dd)
	}
	if !strings.Contains(cfg.Projects["shiny-r"].Notes, "[not a section]") {
		t.Errorf("notes = %q", cfg.Projects["shiny-r"].Notes)
	}
	if cfg.Projects["shiny"].DuplicateOf != "shiny-r" {
		t.Errorf("duplicate_of = %q", cfg.Projects["shiny"].DuplicateOf)
	}
}

func TestSharedTargetsAreResolved(t *testing.T) {
	cfg, err := Load(testutil.WriteConfig(t, sharedTargetsConfig))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]SharedTarget{}
	for _, st := range cfg.SharedTargets() {
		got[st.Source+" "+st.Target] = st
	}
	if len(got) != 3 {
		t.Fatalf("shared targets = %+v", got)
	}
	// duplicate_of leaves one original.
	if st := got["github rstudio/shiny"]; st.Keeper != "shiny-r" || !st.Resolved {
		t.Errorf("rstudio/shiny = %+v", st)
	}
	// [data_dict.keep] says.
	if st := got["openvsx posit/shiny"]; st.Keeper != "shiny-python" || !st.Resolved {
		t.Errorf("posit/shiny = %+v", st)
	}
	// Nothing says: a guess.
	if st := got["plausible shiny.posit.co"]; st.Keeper != "shiny-python" || st.Resolved {
		t.Errorf("shiny.posit.co = %+v", st)
	}

	canonical := map[string]bool{}
	for _, p := range cfg.ProjectInfos() {
		for _, t := range p.Targets {
			canonical[p.ID+" "+t.Source+" "+t.Target] = t.Canonical
		}
	}
	want := map[string]bool{
		"shiny github rstudio/shiny":             false,
		"shiny-r github rstudio/shiny":           true,
		"shiny-r openvsx posit/shiny":            false,
		"shiny-python openvsx posit/shiny":       true,
		"shiny-python github posit-dev/py-shiny": true,
		"shiny-r plausible shiny.posit.co":       false,
		"shiny-python plausible shiny.posit.co":  true,
	}
	for key, w := range want {
		if c, ok := canonical[key]; !ok || c != w {
			t.Errorf("%s: canonical = %v (present %v), want %v", key, c, ok, w)
		}
	}
}

func TestDataDictConfigIsValidated(t *testing.T) {
	cases := map[string]struct{ config, want string }{
		"duplicate of unknown": {`
[projects.a]
github = "o/a"
duplicate_of = "b"
`, `duplicate_of is "b", which isn't a project`},
		"duplicate with own target": {`
[projects.a]
github = ["o/a", "o/x"]
duplicate_of = "b"

[projects.b]
github = "o/a"
`, `which doesn't track github o/x`},
		"keep by other project": {`
[projects.a]
github = "o/a"

[projects.b]
github = "o/b"

[data_dict.keep.github]
"o/a" = "b"
`, `"o/a" is kept by "b", which doesn't track it`},
		"keep unknown source": {`
[data_dict.keep.gitlab]
"o/a" = "b"
`, `data_dict.keep.gitlab: unknown source`},
		"note on unknown table": {`
[[data_dict.notes]]
tables = ["downloads"]
text = "x"
`, `unknown table "downloads"`},
		"note without text": {`
[[data_dict.notes]]
tables = ["metrics"]
`, `text is empty`},
		"definition twice": {`
[[data_dict.definitions]]
table = "metrics"
name = "x"
expr = "value > 1"

[[data_dict.definitions]]
table = "metrics"
name = "x"
expr = "value > 2"
`, `metrics.x is defined twice`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(testutil.WriteConfig(t, c.config))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

// Updating a project leaves multi-line strings alone, and unsetting a key
// with a multi-line value removes all of it.
func TestUpdateProjectKeepsMultilineStrings(t *testing.T) {
	path := testutil.WriteConfig(t, `[projects.a]
name = "A"
notes = """
pypi = "not a key"
[projects.b]
"""
description = """
Two
lines"""
github = "o/a"

[projects.b]
name = "B"
`)
	if err := UpdateProject(path, "a", map[string]string{"pypi": "a"}, []string{"description"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	a := cfg.Projects["a"]
	if a.Notes != "pypi = \"not a key\"\n[projects.b]\n" || a.Description != "" || a.PyPI.First() != "a" || a.GitHubEvents.First() != "o/a" {
		t.Errorf("project a = %+v\n%s", a, data)
	}
	if cfg.Projects["b"].Name != "B" {
		t.Errorf("project b = %+v", cfg.Projects["b"])
	}
}
