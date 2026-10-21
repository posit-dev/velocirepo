// Package reconcile compares stored data with the totals the APIs compute
// themselves, which finds missing or duplicated history that no other check
// sees.
package reconcile

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

// Result is a reference total next to the stored total of one project's
// target.
type Result struct {
	Source    string
	Project   string
	Reference source.ReferenceTotal
	Stored    int64
	// Unavailable says why the reference total couldn't be read.
	Unavailable string
}

// Differs reports whether the stored total is further from the reference
// than its tolerance.
func (r Result) Differs() bool {
	d := r.Stored - r.Reference.Value
	if d > 0 && r.Reference.MayExceed {
		return false
	}
	if d < 0 {
		d = -d
	}
	return r.Unavailable == "" && d > r.Reference.Tolerance
}

// recentDays is how far back the busiest day of a target is looked for.
const recentDays = 30

// Run asks each source's reconciler for the reference totals of the targets
// in the config, and compares them with the stored data. A source whose
// reference request fails gets unavailable results, never a match.
func Run(ctx context.Context, cfg *config.Config, dataDir string, now time.Time, reconcilers map[string]source.Reconciler) ([]Result, error) {
	names := make([]string, 0, len(reconcilers))
	for name := range reconcilers {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []Result
	for _, name := range names {
		projectsOf := map[string][]string{}
		var targets []string
		for id, proj := range cfg.Projects {
			for _, t := range proj.SourceValues(name) {
				if _, seen := projectsOf[t]; !seen {
					targets = append(targets, t)
				}
				projectsOf[t] = append(projectsOf[t], id)
			}
		}
		if len(targets) == 0 {
			continue
		}
		sort.Strings(targets)

		refs, err := reconcilers[name].ReferenceTotals(ctx, targets)
		if err != nil {
			for _, t := range targets {
				for _, p := range projectsOf[t] {
					out = append(out, Result{Source: name, Project: p, Reference: source.ReferenceTotal{Target: t}, Unavailable: err.Error()})
				}
			}
			continue
		}
		stored, err := storedTotals(dataDir, name, refs, now)
		if err != nil {
			return out, err
		}
		for _, ref := range refs {
			for _, p := range projectsOf[ref.Target] {
				r := Result{Source: name, Project: p, Reference: ref, Unavailable: ref.Unavailable}
				key := storedKey{p, ref.Target, ref.Kind, ref.Metric}
				r.Stored = stored[key].total
				// An all-time total includes today, which isn't fetched
				// yet: allow for two of the target's busiest recent days.
				r.Reference.Tolerance += 2 * stored[key].busiestDay
				out = append(out, r)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		return a.Reference.Target < b.Reference.Target
	})
	return out, nil
}

type storedKey struct{ project, target, kind, metric string }

type storedTotal struct {
	total int64
	// busiestDay is the most on one day in the last recentDays days.
	busiestDay int64
}

// storedTotals sums a source's stored data per project, target and metric,
// as DuckDB aggregates over all of it.
func storedTotals(dataDir, sourceName string, refs []source.ReferenceTotal, now time.Time) (map[storedKey]storedTotal, error) {
	kinds := map[string]map[string]bool{}
	for _, r := range refs {
		if r.Unavailable != "" {
			continue
		}
		if kinds[r.Kind] == nil {
			kinds[r.Kind] = map[string]bool{}
		}
		kinds[r.Kind][r.Metric] = true
	}
	since := quote(now.AddDate(0, 0, -recentDays).Format("2006-01-02"))
	out := map[storedKey]storedTotal{}
	for kind, metrics := range kinds {
		var query string
		switch kind {
		case "events":
			query = `WITH daily AS (
				SELECT project, target, type AS metric, CAST(datetime AS DATE) AS day, count(*) AS n
				FROM events WHERE source = %[1]s AND type IN (%[2]s) GROUP BY ALL)
				SELECT project, target, metric, sum(n) AS total, coalesce(max(n) FILTER (WHERE day >= DATE %[3]s), 0) AS busiest
				FROM daily GROUP BY ALL`
		case "records":
			query = `SELECT project, target, metric, sum(value) AS total, coalesce(max(value) FILTER (WHERE date >= DATE %[3]s), 0) AS busiest
				FROM metrics WHERE source = %[1]s AND metric IN (%[2]s) GROUP BY ALL`
		default:
			return nil, fmt.Errorf("unknown kind of reference total %q", kind)
		}
		list := make([]string, 0, len(metrics))
		for m := range metrics {
			list = append(list, quote(m))
		}
		sort.Strings(list)
		rows, _, err := store.QueryLive(dataDir, nil, nil, fmt.Sprintf(query, quote(sourceName), strings.Join(list, ", "), since))
		if err != nil {
			if strings.Contains(err.Error(), "does not exist") {
				continue
			}
			return nil, fmt.Errorf("stored %s totals: %w", kind, err)
		}
		for _, row := range rows {
			project, _ := row["project"].(string)
			target, _ := row["target"].(string)
			metric, _ := row["metric"].(string)
			out[storedKey{project, target, kind, metric}] = storedTotal{total: toInt(row["total"]), busiestDay: toInt(row["busiest"])}
		}
	}
	return out, nil
}

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func toInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case uint64:
		return int64(n)
	case fmt.Stringer:
		var x int64
		_, _ = fmt.Sscan(n.String(), &x)
		return x
	}
	return 0
}
