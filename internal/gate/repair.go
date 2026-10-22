package gate

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/posit-dev/velocirepo/internal/archive"
	"github.com/posit-dev/velocirepo/internal/check"
	"github.com/posit-dev/velocirepo/internal/fetch"
)

// Re-fetching GitHub traffic and PyPI is only possible while the APIs still
// have the days: 14 days for traffic, about 180 for pypistats.
const (
	trafficDays = 14
	pypiDays    = 180
)

// repairMethods says how a source's data for a range is repaired: re-fetched
// where the API still has it, and replayed from the archive where it
// doesn't. Snapshots are never re-fetched for a past range, since that
// would store today's values on past dates.
func repairMethods(sourceName string, from, now time.Time) (refetch, replay bool) {
	within := func(days int) bool { return !from.Before(now.AddDate(0, 0, -days)) }
	switch sourceName {
	case "github":
		// Events and the star history are re-fetched; the snapshots of
		// stars and reactions are replayed.
		return true, true
	case "cran", "plausible":
		return true, false
	case "github-traffic":
		return within(trafficDays), !within(trafficDays)
	case "pypi":
		return within(pypiDays), !within(pypiDays)
	}
	return false, true
}

func repairMessage(rep check.Repair) string {
	return fmt.Sprintf("Repair %s\n\n%s: %s\n", rep, check.RepairTrailer, rep)
}

// repair re-fetches and replays a repair's range, writing over what's stored
// for it.
func (r *run) repair(ctx context.Context, rep check.Repair) ([]fetch.Result, *fetch.Stats, []check.Finding) {
	stats := fetch.NewStats(0)
	var results []fetch.Result
	var findings []check.Finding
	from, err := time.Parse("2006-01-02", rep.From)
	if err != nil {
		return nil, stats, []check.Finding{check.NewFinding("gate.repair", check.Error, rep.Source, rep.Project, "", "", "invalid repair: "+err.Error())}
	}
	refetch, replay := repairMethods(rep.Source, from, r.now)

	if refetch {
		opts := fetch.Options{Project: rep.Project, Sources: []string{rep.Source}, StartDate: rep.From, EndDate: rep.To, Repair: true,
			NoConcatenate: true, Quiet: true, Stats: stats, Now: func() time.Time { return r.now }}
		run := r.opts.Fetch
		if run == nil {
			run = func(ctx context.Context, o fetch.Options) ([]fetch.Result, error) {
				return fetch.All(ctx, r.cfg, r.opts.Tokens, o)
			}
		}
		res, err := run(ctx, opts)
		if err != nil {
			res = append(res, fetch.Result{Source: rep.Source, ProjectID: rep.Project, Error: err.Error()})
		}
		results = append(results, res...)
	}

	if replay {
		runs, err := r.repairRuns(ctx, rep)
		switch {
		case err != nil:
			findings = append(findings, check.NewFinding("gate.repair", check.Warning, rep.Source, rep.Project, "", "replay",
				"the archived runs of the range couldn't be read, so they weren't replayed: "+err.Error()))
		case len(runs) == 0:
			findings = append(findings, check.NewFinding("gate.repair", check.Warning, rep.Source, rep.Project, "", "replay",
				"no archived runs in the range, so its archived requests weren't replayed"))
		}
		for _, dir := range runs {
			run, err := archive.ReadRun(dir)
			if err != nil {
				findings = append(findings, check.NewFinding("gate.repair", check.Warning, rep.Source, rep.Project, "", dir, "couldn't read an archived run: "+err.Error()))
				continue
			}
			res, err := fetch.Replay(ctx, r.cfg, run, fetch.ReplayOptions{Source: rep.Source, Project: rep.Project})
			if err != nil {
				findings = append(findings, check.NewFinding("gate.repair", check.Warning, rep.Source, rep.Project, "", run.Manifest.RunID, "couldn't replay: "+err.Error()))
			}
			results = append(results, res...)
		}
	}
	if r.opts.AfterFetch != nil {
		r.opts.AfterFetch(results)
	}
	if r.opts.BuildDB != nil {
		r.opts.BuildDB()
	}
	return results, stats, findings
}

func (r *run) repairRuns(ctx context.Context, rep check.Repair) ([]string, error) {
	dir, err := os.MkdirTemp("", "velocirepo-repair-")
	if err != nil {
		return nil, err
	}
	if r.opts.Runs != nil {
		return r.opts.Runs(ctx, rep, dir)
	}
	if r.opts.Repo == "" {
		return nil, fmt.Errorf("no data repository to download the archive from")
	}
	s := &archive.ReleaseStore{Token: r.opts.RepoToken, Repo: r.opts.Repo, APIURL: r.opts.APIURL, UploadURL: r.opts.APIURL}
	return s.RunsInRange(ctx, rep.Source, rep.Project, rep.From, rep.To, dir)
}
