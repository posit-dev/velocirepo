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

// Options are a reconciliation run's clock, budget and pacing.
type Options struct {
	Now time.Time
	// Budget limits the requests; nil means no limit.
	Budget *Budget
	// Wait paces the per-month requests; nil means the search API's pace.
	Wait func(context.Context) error
}

// Skipped are targets that weren't reconciled because the budget ran out.
type Skipped struct {
	Source  string
	Quota   string
	Targets []string
	// Monthly is set for the per-month check.
	Monthly bool
}

// referenceRequests is implemented by reconcilers that know how many
// requests their all-time totals take.
type referenceRequests interface {
	ReferenceRequests(targets int) int
}

// Run asks each source's reconciler for the all-time reference totals of
// the targets in the config, and for the sources with per-month totals, for
// tonight's share of the targets' last complete month. It compares them with
// the stored data. A source whose reference request fails gets unavailable
// results, never a match. What doesn't fit the budget is skipped, and
// returned.
func Run(ctx context.Context, cfg *config.Config, dataDir string, opts Options, reconcilers map[string]source.Reconciler) ([]Result, []Skipped, error) {
	budget := opts.Budget
	if budget == nil {
		budget = NewBudget(nil)
	}
	wait := opts.Wait
	if wait == nil {
		wait = NewSearchPacer().Wait
	}
	names := make([]string, 0, len(reconcilers))
	for name := range reconcilers {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []Result
	var skipped []Skipped
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
		rec := reconcilers[name]
		meta, _ := source.MetaFor(name)

		requests := len(targets)
		if rr, ok := rec.(referenceRequests); ok {
			requests = rr.ReferenceRequests(len(targets))
		}
		if budget.Allow(meta.ReferenceQuota, requests) {
			refs, err := rec.ReferenceTotals(ctx, targets)
			results, err := compare(dataDir, name, projectsOf, targets, refs, err, opts.Now, time.Time{}, time.Time{})
			if err != nil {
				return out, skipped, err
			}
			out = append(out, results...)
		} else {
			skipped = append(skipped, Skipped{Source: name, Quota: meta.ReferenceQuota, Targets: targets})
		}

		monthly, ok := rec.(source.MonthlyReconciler)
		if !ok {
			continue
		}
		quota := monthly.MonthlyQuota()
		var tonight, over []string
		for _, t := range Tonight(targets, opts.Now) {
			if budget.Allow(quota, monthly.MonthlyRequests()) {
				tonight = append(tonight, t)
			} else {
				over = append(over, t)
			}
		}
		if len(over) > 0 {
			skipped = append(skipped, Skipped{Source: name, Quota: quota, Targets: over, Monthly: true})
		}
		if len(tonight) == 0 {
			continue
		}
		from, through := LastCompleteMonth(opts.Now)
		// Only the search API is paced; it's the only one with a per-minute
		// limit of its own.
		var pace func(context.Context) error
		if quota == SearchQuota {
			pace = wait
		}
		refs, err := monthly.MonthlyReferenceTotals(ctx, tonight, from, through, pace)
		results, err := compare(dataDir, name, projectsOf, tonight, refs, err, opts.Now, from, through)
		if err != nil {
			return out, skipped, err
		}
		out = append(out, results...)
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
	return out, skipped, nil
}

// compare puts reference totals next to the stored totals of the same
// period; zero from and through mean all time.
func compare(dataDir, name string, projectsOf map[string][]string, targets []string, refs []source.ReferenceTotal, refErr error, now, from, through time.Time) ([]Result, error) {
	var out []Result
	if refErr != nil {
		for _, t := range targets {
			for _, p := range projectsOf[t] {
				out = append(out, Result{Source: name, Project: p, Reference: source.ReferenceTotal{Target: t, From: from, Through: through}, Unavailable: refErr.Error()})
			}
		}
		return out, nil
	}
	stored, err := storedTotals(dataDir, name, refs, now, from, through)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		for _, p := range projectsOf[ref.Target] {
			r := Result{Source: name, Project: p, Reference: ref, Unavailable: ref.Unavailable}
			key := storedKey{p, ref.Target, ref.Kind, ref.Metric}
			r.Stored = stored[key].total
			if from.IsZero() {
				// An all-time total includes today, which isn't fetched
				// yet: allow for two of the target's busiest recent days.
				r.Reference.Tolerance += 2 * stored[key].busiestDay
			}
			out = append(out, r)
		}
	}
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
// A non-zero from and through limit them to those days.
func storedTotals(dataDir, sourceName string, refs []source.ReferenceTotal, now, from, through time.Time) (map[storedKey]storedTotal, error) {
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
	eventsPeriod, recordsPeriod := "", ""
	if !from.IsZero() {
		f, t := quote(from.Format("2006-01-02")), quote(through.Format("2006-01-02"))
		eventsPeriod = fmt.Sprintf(" AND CAST(datetime AS DATE) BETWEEN DATE %s AND DATE %s", f, t)
		recordsPeriod = fmt.Sprintf(" AND date BETWEEN DATE %s AND DATE %s", f, t)
	}
	out := map[storedKey]storedTotal{}
	for kind, metrics := range kinds {
		var query string
		switch kind {
		case "events":
			query = `WITH daily AS (
				SELECT project, target, type AS metric, CAST(datetime AS DATE) AS day, count(*) AS n
				FROM events WHERE source = %[1]s AND type IN (%[2]s)%[4]s GROUP BY ALL)
				SELECT project, target, metric, sum(n) AS total, coalesce(max(n) FILTER (WHERE day >= DATE %[3]s), 0) AS busiest
				FROM daily GROUP BY ALL`
		case "records":
			query = `SELECT project, target, metric, sum(value) AS total, coalesce(max(value) FILTER (WHERE date >= DATE %[3]s), 0) AS busiest
				FROM metrics WHERE source = %[1]s AND metric IN (%[2]s)%[5]s GROUP BY ALL`
		default:
			return nil, fmt.Errorf("unknown kind of reference total %q", kind)
		}
		list := make([]string, 0, len(metrics))
		for m := range metrics {
			list = append(list, quote(m))
		}
		sort.Strings(list)
		rows, _, err := store.QueryLive(dataDir, nil, nil, fmt.Sprintf(query, quote(sourceName), strings.Join(list, ", "), since, eventsPeriod, recordsPeriod))
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
