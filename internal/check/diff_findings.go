package check

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

// diffChecks compares the data with an earlier version: a night only adds
// data, so removed records, changed values and removed content entries are
// errors, except within a repair, for removals the source allows, and for
// projects that are no longer configured.
type diffChecks struct{}

func (diffChecks) Name() string { return "diff" }

func (diffChecks) Run(ctx context.Context, c *Context) ([]Finding, error) {
	if c.Diff == nil {
		return nil, nil
	}
	diffs, repairs, err := CompareData(ctx, c.DataDir, *c.Diff)
	if err != nil {
		return nil, err
	}
	return DiffFindings(c.Config, diffs, repairs), nil
}

// tally counts the items of one finding.
type tally struct {
	check             string
	severity          Severity
	src, project      string
	target, key       string
	dir               string
	what              string
	labels            map[string]int
	count             int
	firstDay, lastDay string
	note              string
}

func (t *tally) add(it item) {
	t.count++
	t.labels[it.label]++
	if it.date != "" {
		if t.firstDay == "" || it.date < t.firstDay {
			t.firstDay = it.date
		}
		if it.date > t.lastDay {
			t.lastDay = it.date
		}
	}
}

func (t *tally) finding() Finding {
	labels := make([]string, 0, len(t.labels))
	for l := range t.labels {
		labels = append(labels, l)
	}
	sort.Slice(labels, func(i, j int) bool {
		if t.labels[labels[i]] != t.labels[labels[j]] {
			return t.labels[labels[i]] > t.labels[labels[j]]
		}
		return labels[i] < labels[j]
	})
	parts := make([]string, 0, len(labels))
	for i, l := range labels {
		if i == 5 {
			parts = append(parts, fmt.Sprintf("%d more", len(labels)-5))
			break
		}
		parts = append(parts, fmt.Sprintf("%s: %s", l, thousands(t.labels[l])))
	}
	msg := fmt.Sprintf("%s %s (%s)", thousands(t.count), t.what, strings.Join(parts, ", "))
	if t.firstDay != "" {
		if t.firstDay == t.lastDay {
			msg += " on " + t.firstDay
		} else {
			msg += fmt.Sprintf(", %s to %s", t.firstDay, t.lastDay)
		}
	}
	if t.note != "" {
		msg += "; " + t.note
	}
	return NewFinding(t.check, t.severity, t.src, t.project, t.target, t.key, msg).WithPaths(t.dir + "/")
}

func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// noun is what a category's items are called.
func noun(category string) string {
	switch category {
	case store.EventsDir:
		return "events"
	case store.ContentDataDir:
		return "content entries"
	}
	return "records"
}

// DiffFindings turns the differences between two versions of the data into
// findings.
func DiffFindings(cfg *config.Config, diffs []ProjectDiff, repairs []Repair) []Finding {
	tallies := map[string]*tally{}
	var order []string
	count := func(pd ProjectDiff, check string, severity Severity, key, what, note string, it item) {
		id := strings.Join([]string{check, pd.Category, pd.Source, pd.Project, it.target, key}, "\x00")
		t, ok := tallies[id]
		if !ok {
			t = &tally{check: check, severity: severity, src: pd.Source, project: pd.Project, target: it.target, key: key, dir: pd.Dir, what: what, labels: map[string]int{}, note: note}
			tallies[id] = t
			order = append(order, id)
		}
		t.add(it)
	}

	for _, pd := range diffs {
		configured := cfg == nil || hasProject(cfg, pd.Project)
		removable := map[string]bool{}
		if pd.Category == store.EventsDir {
			if m, ok := source.MetaFor(pd.Source); ok {
				for _, t := range m.RemovableEvents {
					removable[t] = true
				}
			}
		}
		renamedTo := ""
		if !configured {
			renamedTo = renameTarget(pd, diffs)
		}
		inRepair := func(it item) (Repair, bool) {
			for _, r := range repairs {
				date := it.date
				if pd.Category == store.ContentDataDir {
					date = ""
				}
				if r.Covers(pd.Source, pd.Project, date) {
					return r, true
				}
			}
			return Repair{}, false
		}

		removedCheck := "diff.records-removed"
		if pd.Category == store.ContentDataDir {
			removedCheck = "diff.content-removed"
		}
		for _, it := range pd.removed {
			what := noun(pd.Category) + " removed"
			switch r, ok := inRepair(it); {
			case ok:
				count(pd, "diff.repaired", Warning, r.String(), what+" by the repair "+r.String(), "", it)
			case !configured && renamedTo != "":
				count(pd, removedCheck, Info, pd.Category+" renamed", what, "the project was renamed to "+renamedTo, it)
			case !configured:
				count(pd, removedCheck, Info, pd.Category+" unconfigured", what, "the project is no longer in the config", it)
			case removable[it.label]:
				count(pd, removedCheck, Warning, pd.Category+" removable", what, "deleted upstream", it)
			default:
				count(pd, removedCheck, Error, pd.Category, what, "", it)
			}
		}
		for _, it := range pd.duplicated {
			if r, ok := inRepair(it); ok {
				count(pd, "diff.repaired", Warning, r.String(), "duplicates added by the repair "+r.String(), "", it)
				continue
			}
			// Duplicates in new records; those that were stored already are
			// validate-data's, as warnings.
			count(pd, "data.integrity", Error, pd.Category+" duplicates added", noun(pd.Category)+" stored twice", "", it)
		}
		for _, it := range pd.changed {
			if r, ok := inRepair(it); ok {
				count(pd, "diff.repaired", Warning, r.String(), "values changed by the repair "+r.String(), "", it)
				continue
			}
			count(pd, "diff.values-changed", Error, pd.Category, "stored values changed", "", it)
		}
	}

	out := make([]Finding, 0, len(order))
	for _, id := range order {
		out = append(out, tallies[id].finding())
	}
	return out
}

func hasProject(cfg *config.Config, id string) bool {
	_, ok := cfg.Projects[id]
	return ok
}

// renameTarget returns the project most of a removed project's items moved
// to, with otherwise the same keys, or "".
func renameTarget(pd ProjectDiff, diffs []ProjectDiff) string {
	best, bestCount := "", 0
	for _, other := range diffs {
		if other.Category != pd.Category || other.Source != pd.Source || other.Project == pd.Project {
			continue
		}
		n := 0
		for _, key := range pd.removedKeys {
			if other.addedKeys[key] {
				n++
			}
		}
		if n > bestCount {
			best, bestCount = other.Project, n
		}
	}
	if bestCount*2 < len(pd.removedKeys) {
		return ""
	}
	return best
}
