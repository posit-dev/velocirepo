package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/posit-dev/velocirepo/internal/sourceinfo"
	"github.com/posit-dev/velocirepo/internal/store"
)

// DataDictConfig is what an instance adds to the data dictionary that
// `export --data-dict` writes: everything velocirepo can't know from the
// schema, the sources and the data.
type DataDictConfig struct {
	Name        string `toml:"name"`
	Label       string `toml:"label"`
	Description string `toml:"description"`
	Details     string `toml:"details"`
	// Keep names, per source and target, the project that keeps a target
	// more than one project tracks; the other copies are left out by the
	// dictionary's deduplicated filters.
	Keep map[string]map[string]string `toml:"keep"`
	// Values describe values of enum columns, per table and column, such as
	// the content types an RSS feed sets.
	Values      map[string]map[string]map[string]string `toml:"values"`
	Notes       []DataDictNote                          `toml:"notes"`
	Definitions []DataDictDefinition                    `toml:"definitions"`
	Glossary    map[string]string                       `toml:"glossary"`
}

// DataDictNote is a caveat about the data of one or more tables, such as
// days that are missing.
type DataDictNote struct {
	Tables []string `toml:"tables"`
	// Source and Column are optional: a note about one source's rows, or
	// about one column.
	Source string `toml:"source"`
	Column string `toml:"column"`
	Text   string `toml:"text"`
}

// DataDictDefinition is a named expression on a table, a filter or a
// metric, in data-dict's expression language.
type DataDictDefinition struct {
	Table       string `toml:"table"`
	Name        string `toml:"name"`
	Label       string `toml:"label"`
	Description string `toml:"description"`
	Details     string `toml:"details"`
	Expr        string `toml:"expr"`
}

// DataDictTables are the tables of an export, which notes and definitions
// can refer to.
var DataDictTables = []string{"projects", "targets", "metrics", "metrics_filled", "indicators", "events", "content", "meta"}

// SharedTarget is a target that more than one project tracks.
type SharedTarget struct {
	Source   string
	Target   string
	Projects []string
	// Keeper is the project whose copy counts. Resolved is false when the
	// config doesn't say, and Keeper is a guess: of the projects that aren't
	// duplicates, the one with the most targets, which is usually the
	// curated one, or the first in alphabetical order.
	Keeper   string
	Resolved bool
}

// SharedTargets returns the targets that more than one project tracks,
// sorted by source and target.
func (c *Config) SharedTargets() []SharedTarget {
	trackers := map[[2]string][]string{}
	for id, p := range c.Projects {
		for _, s := range p.Sources() {
			for _, t := range s.Values {
				key := [2]string{s.Name, t}
				trackers[key] = append(trackers[key], id)
			}
		}
	}
	var shared []SharedTarget
	for key, projects := range trackers {
		if len(projects) < 2 {
			continue
		}
		sort.Strings(projects)
		st := SharedTarget{Source: key[0], Target: key[1], Projects: projects}
		if keeper := c.DataDict.Keep[key[0]][key[1]]; keeper != "" {
			st.Keeper, st.Resolved = keeper, true
		} else {
			var originals []string
			for _, id := range projects {
				if c.Projects[id].DuplicateOf == "" {
					originals = append(originals, id)
				}
			}
			switch len(originals) {
			case 1:
				st.Keeper, st.Resolved = originals[0], true
			case 0:
				st.Keeper = c.mostTargets(projects)
			default:
				st.Keeper = c.mostTargets(originals)
			}
		}
		shared = append(shared, st)
	}
	sort.Slice(shared, func(i, j int) bool {
		if shared[i].Source != shared[j].Source {
			return shared[i].Source < shared[j].Source
		}
		return shared[i].Target < shared[j].Target
	})
	return shared
}

// mostTargets returns the project with the most targets, the first in
// alphabetical order of those with as many.
func (c *Config) mostTargets(projects []string) string {
	best, most := "", -1
	for _, id := range projects {
		n := 0
		for _, s := range c.Projects[id].Sources() {
			n += len(s.Values)
		}
		if n > most {
			best, most = id, n
		}
	}
	return best
}

// keepers maps every shared source and target to the project that keeps it.
func (c *Config) keepers() map[[2]string]string {
	keepers := map[[2]string]string{}
	for _, st := range c.SharedTargets() {
		keepers[[2]string{st.Source, st.Target}] = st.Keeper
	}
	return keepers
}

// projectTargets returns a project's targets, saying for each whether the
// project's copy is the one that counts.
func (c *Config) projectTargets(id string, keepers map[[2]string]string) []store.ProjectTarget {
	var targets []store.ProjectTarget
	for _, s := range c.Projects[id].Sources() {
		for _, t := range s.Values {
			keeper, shared := keepers[[2]string{s.Name, t}]
			targets = append(targets, store.ProjectTarget{
				Source:    s.Name,
				Target:    t,
				Canonical: !shared || keeper == id,
			})
		}
	}
	return targets
}

// validateDataDict checks what [data_dict], notes and duplicate_of refer to.
// Which columns exist is only known when exporting, so columns are checked
// there.
func (c *Config) validateDataDict() error {
	var problems []string
	ids := make([]string, 0, len(c.Projects))
	for id := range c.Projects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		p := c.Projects[id]
		if p.DuplicateOf == "" {
			continue
		}
		other, ok := c.Projects[p.DuplicateOf]
		switch {
		case p.DuplicateOf == id:
			problems = append(problems, fmt.Sprintf("projects.%s.duplicate_of names the project itself", id))
		case !ok:
			problems = append(problems, fmt.Sprintf("projects.%s.duplicate_of is %q, which isn't a project", id, p.DuplicateOf))
		default:
			for _, s := range p.Sources() {
				for _, t := range s.Values {
					if !contains(other.SourceValues(s.Name), t) {
						problems = append(problems, fmt.Sprintf("projects.%s.duplicate_of is %q, which doesn't track %s %s", id, p.DuplicateOf, s.Name, t))
					}
				}
			}
		}
	}

	for src, targets := range c.DataDict.Keep {
		if _, ok := sourceinfo.Get(src); !ok {
			problems = append(problems, fmt.Sprintf("data_dict.keep.%s: unknown source", src))
			continue
		}
		for target, keeper := range targets {
			p, ok := c.Projects[keeper]
			if !ok {
				problems = append(problems, fmt.Sprintf("data_dict.keep.%s: %q is kept by %q, which isn't a project", src, target, keeper))
				continue
			}
			if !contains(p.SourceValues(src), target) {
				problems = append(problems, fmt.Sprintf("data_dict.keep.%s: %q is kept by %q, which doesn't track it", src, target, keeper))
			}
		}
	}

	tables := map[string]bool{}
	for _, t := range DataDictTables {
		tables[t] = true
	}
	for i, n := range c.DataDict.Notes {
		where := fmt.Sprintf("data_dict.notes[%d]", i)
		if len(n.Tables) == 0 {
			problems = append(problems, where+": tables is empty")
		}
		for _, t := range n.Tables {
			if !tables[t] {
				problems = append(problems, fmt.Sprintf("%s: unknown table %q (use %s)", where, t, strings.Join(DataDictTables, ", ")))
			}
		}
		if n.Source != "" {
			if _, ok := sourceinfo.Get(n.Source); !ok {
				problems = append(problems, fmt.Sprintf("%s: unknown source %q", where, n.Source))
			}
		}
		if strings.TrimSpace(n.Text) == "" {
			problems = append(problems, where+": text is empty")
		}
	}
	seen := map[string]bool{}
	for i, d := range c.DataDict.Definitions {
		where := fmt.Sprintf("data_dict.definitions[%d]", i)
		if !tables[d.Table] {
			problems = append(problems, fmt.Sprintf("%s: unknown table %q", where, d.Table))
		}
		if d.Name == "" || strings.TrimSpace(d.Expr) == "" {
			problems = append(problems, where+": name and expr are required")
		}
		if seen[d.Table+"."+d.Name] {
			problems = append(problems, fmt.Sprintf("%s: %s.%s is defined twice", where, d.Table, d.Name))
		}
		seen[d.Table+"."+d.Name] = true
	}
	for table := range c.DataDict.Values {
		if !tables[table] {
			problems = append(problems, fmt.Sprintf("data_dict.values.%s: unknown table", table))
		}
	}
	for term := range c.DataDict.Glossary {
		if _, ok := c.Projects[term]; ok && c.Projects[term].Notes != "" {
			problems = append(problems, fmt.Sprintf("data_dict.glossary.%s: the project's notes already define this term", term))
		}
	}

	sort.Strings(problems)
	if len(problems) > 0 {
		return fmt.Errorf("data dictionary settings:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
