package check

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/fetch"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

// Context is what the checks look at.
type Context struct {
	Config  *config.Config
	DataDir string
	Now     time.Time
	// Results are what a fetch run did, if known: from fetch --results, or
	// from the fetch log of a past run.
	Results *fetch.RunResults
	// Diff, if set, compares the data with an earlier version.
	Diff *Diff
	// Reconcilers give the reference totals of the sources that have them;
	// without them, nothing is reconciled.
	Reconcilers map[string]source.Reconciler
	// ReconcileWait paces the per-month reference requests; nil means the
	// search API's pace.
	ReconcileWait func(context.Context) error
}

// Checker is one check.
type Checker interface {
	Name() string
	Run(ctx context.Context, c *Context) ([]Finding, error)
}

// Default are the checks velocirepo check runs.
func Default() []Checker {
	return []Checker{fetchFailed{}, deprecations{}, diffChecks{}, integrity{}, volume{}, freshness{}, reconciliation{}}
}

// Run runs checks, marks accepted findings, and returns them sorted by
// severity, check, source, project and target.
func Run(ctx context.Context, c *Context, checks []Checker, accepted Acceptances) (Report, error) {
	var report Report
	for _, chk := range checks {
		findings, err := chk.Run(ctx, c)
		if err != nil {
			return report, fmt.Errorf("%s: %w", chk.Name(), err)
		}
		report.Findings = append(report.Findings, findings...)
	}
	for i := range report.Findings {
		if a, ok := accepted.Match(report.Findings[i], c.Now); ok {
			report.Findings[i].Accepted = true
			report.Findings[i].Reason = a.Reason
		}
	}
	sortFindings(report.Findings)
	return report, nil
}

var severityOrder = map[Severity]int{Error: 0, Warning: 1, Info: 2}

func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if severityOrder[a.Severity] != severityOrder[b.Severity] {
			return severityOrder[a.Severity] < severityOrder[b.Severity]
		}
		for _, pair := range [][2]string{{a.Check, b.Check}, {a.Source, b.Source}, {a.Project, b.Project}, {a.Target, b.Target}, {a.Key, b.Key}} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return false
	})
}

// fetchFailed reports jobs that failed. A failed job stores nothing, so the
// next run fetches the same days again; it's an error because it needs
// attention, not because there's data to hold back.
type fetchFailed struct{}

func (fetchFailed) Name() string { return "fetch.failed" }

func (fetchFailed) Run(_ context.Context, c *Context) ([]Finding, error) {
	if c.Results == nil {
		return nil, nil
	}
	var out []Finding
	for _, r := range c.Results.Results {
		if r.Error == "" {
			continue
		}
		out = append(out, NewFinding("fetch.failed", Error, r.Source, r.ProjectID, "", "", r.Error))
	}
	return out, nil
}

// deprecations reports endpoints that announced their removal.
type deprecations struct{}

func (deprecations) Name() string { return "fetch.deprecated" }

func (deprecations) Run(_ context.Context, c *Context) ([]Finding, error) {
	if c.Results == nil {
		return nil, nil
	}
	var out []Finding
	for _, d := range c.Results.Deprecations {
		endpoint, _, _ := strings.Cut(d, ":")
		out = append(out, NewFinding("fetch.deprecated", Warning, "", "", "", endpoint, "an API endpoint announced its removal: "+d))
	}
	return out, nil
}

// integrity runs validate-data. Problems in stored data are warnings; the
// gate makes them errors when they're about the night's new records.
type integrity struct{}

func (integrity) Name() string { return "data.integrity" }

func (integrity) Run(_ context.Context, c *Context) ([]Finding, error) {
	projects := map[string]bool{}
	if c.Config != nil {
		for id := range c.Config.Projects {
			projects[id] = true
		}
	}
	result, err := store.ValidateData(c.DataDir, projects)
	if err != nil {
		return nil, err
	}

	type group struct {
		issue store.Issue
		count int
	}
	groups := map[string]*group{}
	var keys []string
	for _, issue := range result.Issues {
		key := issue.Type.String() + "\x00" + issue.Path
		if g, ok := groups[key]; ok {
			g.count++
			continue
		}
		groups[key] = &group{issue: issue, count: 1}
		keys = append(keys, key)
	}

	var out []Finding
	for _, key := range keys {
		g := groups[key]
		rel, err := filepath.Rel(c.DataDir, g.issue.Path)
		if err != nil {
			rel = g.issue.Path
		}
		src, project := sourceAndProject(rel)
		msg := g.issue.Message
		if g.count > 1 {
			msg = fmt.Sprintf("%s: %d times in %s", g.issue.Type, g.count, rel)
		}
		out = append(out, NewFinding("data.integrity", Warning, src, project, "", g.issue.Type.String()+" "+rel, msg).WithPaths(rel))
	}
	return out, nil
}

// sourceAndProject reads the source and project from a path relative to the
// data directory (<category>/<source>/<project>/...).
func sourceAndProject(rel string) (string, string) {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 3 {
		return "", ""
	}
	return parts[1], parts[2]
}

// volume reports targets that usually have data and suddenly have none: a
// source that silently stopped returning data. "Usually" is at least 20 of
// the 28 days before the last three.
type volume struct{}

func (volume) Name() string { return "data.volume" }

const (
	volumeBaselineDays = 28
	volumeMinDays      = 20
	volumeRecentDays   = 3
)

func (volume) Run(_ context.Context, c *Context) ([]Finding, error) {
	today := c.Now.UTC().Truncate(24 * time.Hour)
	recentStart := today.AddDate(0, 0, -volumeRecentDays)
	baseStart := recentStart.AddDate(0, 0, -volumeBaselineDays)
	query := fmt.Sprintf(`
		WITH days AS (
			SELECT DISTINCT source, project, target, date AS day
			FROM metrics
			WHERE metric LIKE 'daily_%%' AND date >= DATE '%s' AND date < DATE '%s'
		)
		SELECT source, project, target,
			count(*) FILTER (WHERE day < DATE '%s') AS base_days,
			count(*) FILTER (WHERE day >= DATE '%s') AS recent_days
		FROM days
		GROUP BY ALL
		HAVING base_days >= %d AND recent_days = 0
		ORDER BY ALL`,
		baseStart.Format("2006-01-02"), today.Format("2006-01-02"),
		recentStart.Format("2006-01-02"), recentStart.Format("2006-01-02"), volumeMinDays)
	rows, _, err := store.QueryLive(c.DataDir, nil, nil, query)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, row := range rows {
		src, _ := row["source"].(string)
		project, _ := row["project"].(string)
		target, _ := row["target"].(string)
		out = append(out, NewFinding("data.volume", Warning, src, project, target, "",
			fmt.Sprintf("no data for the last %d days, while it had data on %v of the %d days before", volumeRecentDays, row["base_days"], volumeBaselineDays)))
	}
	return out, nil
}
