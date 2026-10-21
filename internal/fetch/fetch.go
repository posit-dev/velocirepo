package fetch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/posit-dev/velocirepo/internal/auth"
	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/dateutil"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/sourceinfo"
	"github.com/posit-dev/velocirepo/internal/store"
	"golang.org/x/sync/errgroup"
)

type Options struct {
	Project string
	// Sources limits the fetch to these sources; empty means all.
	Sources       []string
	StartDate     string
	EndDate       string
	NoConcatenate bool
	Quiet         bool
	OnResult      ResultCallback
	Stats         *Stats
	// Now returns the current time; nil means time.Now. It decides the
	// default end date, the GitHub traffic window and which periods
	// compaction may close, so tests can run the orchestrator on a simulated
	// clock.
	Now func() time.Time
	// WrapTransport wraps the HTTP transport every source uses, for example
	// to capture responses for the archive.
	WrapTransport func(http.RoundTripper) http.RoundTripper
	// OnJob is called for every target of a job with its resolved options,
	// before it's fetched.
	OnJob func(JobInfo)
	// OnTargetDone is called after a target was fetched successfully, with
	// its source, for example to collect what the archive's manifest needs.
	OnTargetDone func(JobInfo, any)
	// Repair re-fetches StartDate through EndDate and replaces what's stored
	// for those days, so a day can also become "no data". Snapshots, whose
	// values can't be fetched for a past day, are left out, and watermarks
	// don't move.
	Repair bool
	// Concatenate replaces the concatenation after the fetch; nil uses
	// ConcatenationPolicy. It isn't called with NoConcatenate.
	Concatenate func(dataDir string, now time.Time) error
}

// JobInfo describes one target of a fetch job and the options it runs with.
type JobInfo struct {
	Source  string
	Project string
	Target  string
	Options source.FetchOptions
	// Zones are the time zones the target's series end their days in.
	Zones map[string]string
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

type Result struct {
	Source     string        `json:"source"`
	ProjectID  string        `json:"project_id"`
	Records    int           `json:"records"`
	Files      int           `json:"files,omitempty"`
	StartDate  string        `json:"start_date"`
	EndDate    string        `json:"end_date"`
	Duration   time.Duration `json:"duration,omitempty"`
	StatusCode int           `json:"status_code,omitempty"`
	Skipped    string        `json:"skipped,omitempty"`
	Error      string        `json:"error,omitempty"`
}

type Tokens struct {
	GitHub    string
	Plausible string
	YouTube   string
	LinkedIn  string
}

func TokensFromEnv() Tokens {
	return Tokens{
		GitHub:    os.Getenv("GITHUB_TOKEN"),
		Plausible: os.Getenv("PLAUSIBLE_TOKEN"),
		YouTube:   os.Getenv("YOUTUBE_TOKEN"),
		LinkedIn:  os.Getenv("LINKEDIN_TOKEN"),
	}
}

func resolveEndDate(cfg *config.Config, endDateStr string, now time.Time) (time.Time, error) {
	if endDateStr != "" {
		return dateutil.ParseDate(endDateStr)
	}
	if cfg.Settings.EndDate == "yesterday" || cfg.Settings.EndDate == "" {
		return now.AddDate(0, 0, -1).Truncate(24 * time.Hour), nil
	}
	return dateutil.ParseDate(cfg.Settings.EndDate)
}

func resolveStartDate(dataDir, sourceName, projectID, startDateStr string, now time.Time) (time.Time, error) {
	if startDateStr != "" {
		return dateutil.ParseDate(startDateStr)
	}
	last, err := store.LastDate(dataDir, sourceName, projectID)
	if err != nil {
		return time.Time{}, err
	}
	var start time.Time
	if last.IsZero() {
		start = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	} else {
		start = last.AddDate(0, 0, 1)
	}

	if sourceName == "github-traffic" {
		earliest := now.AddDate(0, 0, -14).Truncate(24 * time.Hour)
		if start.Before(earliest) {
			if !last.IsZero() {
				slog.Warn("github traffic only covers the last 14 days; earlier days can no longer be fetched",
					"project", projectID, "from", dateutil.FormatDate(start), "to", dateutil.FormatDate(earliest.AddDate(0, 0, -1)))
			}
			start = earliest
		}
	}

	return start, nil
}

func filterProjects(projects map[string]config.Project, projectID string) map[string]config.Project {
	if projectID == "" {
		return projects
	}
	p, ok := projects[projectID]
	if !ok {
		return nil
	}
	return map[string]config.Project{projectID: p}
}

type fetchJob struct {
	sourceName string
	projectID  string
	sources    []source.Source
	// targets holds each metric source's target; "" for sources without
	// one, which resume per project.
	targets      []string
	eventSources []source.EventSource
	eventTargets []string
}

type jobKey struct {
	sourceName string
	projectID  string
}

func addMetricJob(jobs map[jobKey]*fetchJob, order *[]jobKey, sourceName, projectID, target string, src source.Source) {
	key := jobKey{sourceName: sourceName, projectID: projectID}
	job := jobs[key]
	if job == nil {
		job = &fetchJob{sourceName: sourceName, projectID: projectID}
		jobs[key] = job
		*order = append(*order, key)
	}
	job.sources = append(job.sources, src)
	job.targets = append(job.targets, target)
}

func addEventJob(jobs map[jobKey]*fetchJob, order *[]jobKey, sourceName, projectID, target string, src source.EventSource) {
	key := jobKey{sourceName: sourceName, projectID: projectID}
	job := jobs[key]
	if job == nil {
		job = &fetchJob{sourceName: sourceName, projectID: projectID}
		jobs[key] = job
		*order = append(*order, key)
	}
	job.eventSources = append(job.eventSources, src)
	job.eventTargets = append(job.eventTargets, target)
}

func orderedJobs(jobs map[jobKey]*fetchJob, order []jobKey) []fetchJob {
	out := make([]fetchJob, 0, len(order))
	for _, key := range order {
		out = append(out, *jobs[key])
	}
	return out
}

func selectedProjects(cfg *config.Config, opts Options) (map[string]config.Project, error) {
	projects := cfg.Projects
	if len(projects) == 0 {
		return nil, fmt.Errorf("no projects configured")
	}

	projects = filterProjects(projects, opts.Project)
	if projects == nil {
		return nil, fmt.Errorf("project %q not found in config", opts.Project)
	}
	return projects, nil
}

type ResultCallback func(Result)

func runJobs(ctx context.Context, cfg *config.Config, opts Options, jobs []fetchJob, concurrency int) ([]Result, error) {
	return runJobsWithCallback(ctx, cfg, opts, jobs, concurrency, opts.OnResult)
}

func runJobsWithCallback(ctx context.Context, cfg *config.Config, opts Options, jobs []fetchJob, concurrency int, onResult ResultCallback) ([]Result, error) {
	endDate, err := resolveEndDate(cfg, opts.EndDate, opts.now())
	if err != nil {
		return nil, fmt.Errorf("parse end date: %w", err)
	}
	plan := &planner{dataDir: cfg.DataDir(), now: opts.now(), repair: opts.Repair}
	if opts.Repair && (opts.StartDate == "" || opts.EndDate == "") {
		return nil, fmt.Errorf("a repair needs a start and end date")
	}
	if opts.StartDate != "" {
		startDate, err := dateutil.ParseDate(opts.StartDate)
		if err != nil {
			return nil, fmt.Errorf("parse start date: %w", err)
		}
		if startDate.After(endDate) {
			return nil, fmt.Errorf("start date %s is after end date %s", opts.StartDate, dateutil.FormatDate(endDate))
		}
		plan.start = startDate
	}
	if opts.EndDate != "" || (cfg.Settings.EndDate != "" && cfg.Settings.EndDate != "yesterday") {
		plan.end = endDate
	}

	dataDir := cfg.DataDir()
	if concurrency <= 0 {
		concurrency = 1
	}

	resultsCh := make(chan []Result, len(jobs))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)

	for _, job := range jobs {
		job := job
		g.Go(func() error {
			resultsCh <- runJob(gctx, plan, job, hooks{onJob: opts.OnJob, onDone: opts.OnTargetDone})
			return nil
		})
	}

	var results []Result
	done := make(chan struct{})
	go func() {
		for jobResults := range resultsCh {
			results = append(results, jobResults...)
			for _, r := range jobResults {
				if opts.Stats != nil {
					opts.Stats.AddResult(r)
				}
				if onResult != nil {
					onResult(r)
				}
			}
		}
		close(done)
	}()

	_ = g.Wait()
	close(resultsCh)
	<-done

	if !opts.NoConcatenate {
		concatenate := func(dataDir string, now time.Time) error {
			return store.AggregateWithPolicy(dataDir, now, ConcatenationPolicy(cfg, dataDir, now))
		}
		if opts.Concatenate != nil {
			concatenate = opts.Concatenate
		}
		if err := concatenate(dataDir, opts.now()); err != nil {
			return results, fmt.Errorf("concatenation: %w", err)
		}
	}

	return results, nil
}

// hooks are the callbacks a job reports its targets to.
type hooks struct {
	onJob  func(JobInfo)
	onDone func(JobInfo, any)
}

func (h hooks) start(info JobInfo) {
	if h.onJob != nil {
		h.onJob(info)
	}
}

func (h hooks) done(info JobInfo, src any) {
	if h.onDone != nil {
		h.onDone(info, src)
	}
}

func targetContext(ctx context.Context, info JobInfo) context.Context {
	return source.WithRequestInfo(ctx, source.RequestInfo{Source: info.Source, Project: info.Project, Target: info.Target})
}

func runJob(ctx context.Context, plan *planner, job fetchJob, h hooks) []Result {
	started := time.Now()
	if len(job.eventSources) > 0 {
		return runEventJob(ctx, plan, job, h, started)
	}
	if len(job.sources) == 0 {
		return []Result{{
			Source:    job.sourceName,
			ProjectID: job.projectID,
			Duration:  time.Since(started),
			Error:     "no fetcher configured",
		}}
	}
	return runMetricJob(ctx, plan, job, h, started)
}

func jobError(job fetchJob, started time.Time, format string, err error) []Result {
	return []Result{{
		Source:    job.sourceName,
		ProjectID: job.projectID,
		Duration:  time.Since(started),
		Error:     fmt.Sprintf(format, err),
	}}
}

// eventPlan is one target of an event job, with the days it fetches per
// series.
type eventPlan struct {
	info   JobInfo
	target string
	src    source.EventSource
	opts   source.FetchOptions
	events source.Window
	stars  source.Window
}

func runEventJob(ctx context.Context, plan *planner, job fetchJob, h hooks, started time.Time) []Result {
	dataDir := plan.dataDir
	mainMarks, err := store.SeriesWatermarks(dataDir, job.sourceName, job.projectID, "")
	if err != nil {
		return jobError(job, started, "read watermarks: %v", err)
	}
	starMarks, err := store.SeriesWatermarks(dataDir, job.sourceName, job.projectID, "stars")
	if err != nil {
		return jobError(job, started, "read watermarks: %v", err)
	}

	var plans []eventPlan
	for i, src := range job.eventSources {
		target := job.eventTargets[i]
		events, eventsZone, err := plan.window(ctx, job, src, target, "", mainMarks)
		if err != nil {
			return jobError(job, started, "plan: %v", err)
		}
		stars, starsZone, err := plan.window(ctx, job, src, target, "stars", starMarks)
		if err != nil {
			return jobError(job, started, "plan: %v", err)
		}
		if events.Empty() && stars.Empty() {
			continue
		}
		opts := source.FetchOptions{
			ProjectID:     job.projectID,
			StartDate:     events.Start,
			EndDate:       events.End,
			DataDir:       dataDir,
			Windows:       map[string]source.Window{"stars": stars},
			SkipSnapshots: plan.repair,
		}
		info := JobInfo{Source: job.sourceName, Project: job.projectID, Target: target, Options: opts,
			Zones: map[string]string{"": eventsZone.String(), "stars": starsZone.String()}}
		h.start(info)
		plans = append(plans, eventPlan{info: info, target: target, src: src, opts: opts, events: events, stars: stars})
	}
	if len(plans) == 0 {
		return []Result{{Source: job.sourceName, ProjectID: job.projectID, Duration: time.Since(started), Skipped: "already up to date"}}
	}

	var results []Result
	var events []source.Event
	var records []source.Record
	contentByFilename := make(map[string][]source.ContentEntry)
	for _, pl := range plans {
		fetched, err := pl.src.FetchEvents(targetContext(ctx, pl.info), pl.opts)
		if err != nil {
			results = append(results, Result{
				Source:     job.sourceName,
				ProjectID:  job.projectID,
				Duration:   time.Since(started),
				StatusCode: httpStatusCode(err),
				Error:      err.Error(),
			})
			continue
		}
		h.done(pl.info, pl.src)
		events = append(events, fetched...)
		if mcp, ok := pl.src.(source.MultiContentProvider); ok {
			for filename, entries := range mcp.ContentByFilename() {
				contentByFilename[filename] = append(contentByFilename[filename], entries...)
			}
		}
		if rp, ok := pl.src.(source.RecordProvider); ok {
			records = append(records, rp.Records()...)
		}
	}
	// A job whose target failed stores nothing and doesn't move its
	// watermarks, so the next run fetches the same days again.
	if len(results) > 0 {
		return results
	}

	windows := make(map[string]source.Window)
	for _, pl := range plans {
		if !pl.events.Empty() {
			windows[pl.target] = pl.events
		}
	}

	var filesWritten int
	if len(events) > 0 || len(windows) > 0 {
		n, err := store.WriteEventsScoped(dataDir, job.sourceName, job.projectID, events, windows)
		if err != nil {
			return jobError(job, started, "write: %v", err)
		}
		filesWritten += n
	}
	for filename, entries := range contentByFilename {
		if err := store.WriteContent(dataDir, job.sourceName, job.projectID, filename, entries); err != nil {
			slog.Warn("write content failed", "source", job.sourceName, "project", job.projectID, "error", err)
		} else {
			filesWritten++
		}
	}
	if plan.repair {
		// Only the star history is re-fetched; the snapshots' totals stay.
		scope := store.RecordScope{Windows: map[string]source.Window{}, Metrics: []string{"daily_stars"}}
		var history []source.Record
		for _, pl := range plans {
			scope.Windows[pl.target] = pl.stars
		}
		for _, r := range records {
			if r.Metric == "daily_stars" {
				history = append(history, r)
			}
		}
		n, err := store.WriteRecordsScoped(dataDir, job.sourceName, job.projectID, history, scope)
		if err != nil {
			return jobError(job, started, "write: %v", err)
		}
		filesWritten += n
		records = history
	} else if len(records) > 0 {
		n, err := store.WriteRecords(dataDir, job.sourceName, job.projectID, records)
		if err != nil {
			slog.Warn("write records failed", "source", job.sourceName, "project", job.projectID, "error", err)
		}
		filesWritten += n
	}

	for _, pl := range plans {
		if plan.repair {
			break
		}
		if !pl.events.Empty() {
			markFetched(dataDir, job, "", pl.target, pl.events.End)
		}
		if !pl.stars.Empty() {
			markFetched(dataDir, job, "stars", pl.target, pl.stars.End)
		}
	}

	r := Result{
		Source:    job.sourceName,
		ProjectID: job.projectID,
		Records:   len(events) + len(records),
		Files:     filesWritten,
		Duration:  time.Since(started),
	}
	r.StartDate, r.EndDate = span(plans)
	if len(events) == 0 && len(contentByFilename) == 0 && len(records) == 0 {
		r.Skipped = "no new events"
	}
	return []Result{r}
}

// span returns the earliest start and latest end of a job's targets, for its
// result.
func span(plans []eventPlan) (string, string) {
	var start, end time.Time
	for _, pl := range plans {
		for _, w := range []source.Window{pl.events, pl.stars} {
			if w.Empty() {
				continue
			}
			if start.IsZero() || (!w.Start.IsZero() && w.Start.Before(start)) {
				start = w.Start
			}
			if w.End.After(end) {
				end = w.End
			}
		}
	}
	if start.IsZero() {
		start = beginning
	}
	return dateutil.FormatDate(start), dateutil.FormatDate(end)
}

// window returns the days a target's series is fetched over, and the time
// zone they end in.
func (p *planner) window(ctx context.Context, job fetchJob, src any, target, series string, marks map[string]string) (source.Window, *time.Location, error) {
	start, err := p.startDate(job.sourceName, job.projectID, target, series, marks)
	if err != nil {
		return source.Window{}, nil, err
	}
	zone, err := dayZone(source.WithRequestInfo(ctx, source.RequestInfo{Source: job.sourceName, Project: job.projectID, Target: target}), src, series)
	if err != nil {
		return source.Window{}, nil, err
	}
	return source.Window{Start: start, End: p.endDate(zone, job.sourceName, target)}, zone, nil
}

// markFetched records that a target's series has been fetched through date,
// so the next run resumes after it even when nothing happened.
func markFetched(dataDir string, job fetchJob, series, target string, date time.Time) {
	if target == "" || date.IsZero() {
		return
	}
	if err := store.MarkFetchedSeries(dataDir, job.sourceName, job.projectID, series, dateutil.FormatDate(date), []string{target}); err != nil {
		slog.Warn("record fetched date failed", "source", job.sourceName, "project", job.projectID, "error", err)
	}
}

// metricPlan is one target of a metric job.
type metricPlan struct {
	info   JobInfo
	target string
	src    source.Source
	opts   source.FetchOptions
	// mark is how far the target's watermark moves; zero means it's
	// decided by the latest day returned.
	mark time.Time
}

func runMetricJob(ctx context.Context, plan *planner, job fetchJob, h hooks, started time.Time) []Result {
	dataDir := plan.dataDir
	marks, err := store.SeriesWatermarks(dataDir, job.sourceName, job.projectID, "")
	if err != nil {
		return jobError(job, started, "read watermarks: %v", err)
	}

	var plans []metricPlan
	for i, src := range job.sources {
		target := job.targets[i]
		w, zone, err := plan.window(ctx, job, src, target, "", marks)
		if err != nil {
			return jobError(job, started, "plan: %v", err)
		}
		mark := w.End
		if _, isPublisher := src.(source.Publisher); isPublisher {
			// A source that publishes late is only fetched, and marked,
			// through the day it has published.
			pub := plan.publishedThrough(source.WithRequestInfo(ctx, source.RequestInfo{Source: job.sourceName, Project: job.projectID}), job.sourceName, src)
			switch {
			case !pub.known:
				mark = time.Time{}
			case pub.day.Before(w.End):
				w.End, mark = pub.day, pub.day
			}
		}
		if w.Empty() {
			continue
		}
		opts := source.FetchOptions{ProjectID: job.projectID, StartDate: w.Start, EndDate: w.End, DataDir: dataDir}
		info := JobInfo{Source: job.sourceName, Project: job.projectID, Target: target, Options: opts, Zones: map[string]string{"": zone.String()}}
		h.start(info)
		plans = append(plans, metricPlan{info: info, target: target, src: src, opts: opts, mark: mark})
	}
	if len(plans) == 0 {
		return []Result{{Source: job.sourceName, ProjectID: job.projectID, Duration: time.Since(started), Skipped: "already up to date"}}
	}

	var results []Result
	var records []source.Record
	contentByFilename := make(map[string][]source.ContentEntry)
	for _, pl := range plans {
		fetched, err := pl.src.Fetch(targetContext(ctx, pl.info), pl.opts)
		if err != nil {
			results = append(results, Result{
				Source:     job.sourceName,
				ProjectID:  job.projectID,
				Duration:   time.Since(started),
				StatusCode: httpStatusCode(err),
				Error:      err.Error(),
			})
			continue
		}
		h.done(pl.info, pl.src)
		if len(fetched) == 0 {
			continue
		}
		records = append(records, fetched...)
		if cp, ok := pl.src.(source.ContentProvider); ok {
			if entries := cp.ContentEntries(); len(entries) > 0 {
				contentByFilename[cp.ContentFilename()] = append(contentByFilename[cp.ContentFilename()], entries...)
			}
		}
	}
	if len(results) > 0 {
		return results
	}

	var filesWritten int
	if plan.repair {
		scope := store.RecordScope{Windows: map[string]source.Window{}}
		for _, pl := range plans {
			scope.Windows[pl.target] = source.Window{Start: pl.opts.StartDate, End: pl.opts.EndDate}
		}
		n, err := store.WriteRecordsScoped(dataDir, job.sourceName, job.projectID, records, scope)
		if err != nil {
			return jobError(job, started, "write: %v", err)
		}
		filesWritten = n
	} else if len(records) > 0 {
		n, err := store.WriteRecords(dataDir, job.sourceName, job.projectID, records)
		if err != nil {
			return jobError(job, started, "write: %v", err)
		}
		filesWritten = n
	}
	for filename, entries := range contentByFilename {
		if err := store.WriteContent(dataDir, job.sourceName, job.projectID, filename, entries); err != nil {
			slog.Warn("write content failed", "source", job.sourceName, "project", job.projectID, "error", err)
		} else {
			filesWritten++
		}
	}

	start, end := plans[0].opts.StartDate, plans[0].opts.EndDate
	for _, pl := range plans {
		if pl.opts.StartDate.Before(start) {
			start = pl.opts.StartDate
		}
		if pl.opts.EndDate.After(end) {
			end = pl.opts.EndDate
		}
		if plan.repair {
			continue
		}
		mark := pl.mark
		if mark.IsZero() {
			mark = latestRecordDate(records, pl.target)
		}
		markFetched(dataDir, job, "", pl.target, mark)
	}

	r := Result{
		Source:    job.sourceName,
		ProjectID: job.projectID,
		Records:   len(records),
		Files:     filesWritten,
		StartDate: dateutil.FormatDate(start),
		EndDate:   dateutil.FormatDate(end),
		Duration:  time.Since(started),
	}
	if len(records) == 0 && !plan.repair {
		r.Skipped = "no new records"
	}
	return []Result{r}
}

func httpStatusCode(err error) int {
	var statusErr *source.HTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.Code
	}
	return 0
}

type fetchSourceDescriptor struct {
	sourceinfo.Descriptor
	missingToken  func(Tokens) string
	beforeFetch   func(context.Context, Tokens)
	metricFactory func(*http.Client, Tokens, *config.Config, string) source.Source
	eventFactory  func(*http.Client, Tokens, *config.Config, string) source.EventSource
}

var fetchSourceDescriptors = []fetchSourceDescriptor{
	{
		Descriptor: sourceinfo.Must("github-traffic"),
		metricFactory: func(client *http.Client, tokens Tokens, _ *config.Config, target string) source.Source {
			return &source.GitHubTraffic{Client: client, Token: tokens.GitHub, Repo: target}
		},
	},
	{
		Descriptor: sourceinfo.Must("github"),
		eventFactory: func(client *http.Client, tokens Tokens, _ *config.Config, target string) source.EventSource {
			return &source.GitHubEvents{Client: client, Token: tokens.GitHub, Repo: target}
		},
	},
	{
		Descriptor: sourceinfo.Must("pypi"),
		metricFactory: func(client *http.Client, _ Tokens, _ *config.Config, target string) source.Source {
			return &source.PyPI{Client: client, Package: target}
		},
	},
	{
		Descriptor: sourceinfo.Must("cran"),
		metricFactory: func(client *http.Client, _ Tokens, _ *config.Config, target string) source.Source {
			return &source.CRAN{Client: client, Package: target}
		},
	},
	{
		Descriptor: sourceinfo.Must("homebrew"),
		metricFactory: func(client *http.Client, _ Tokens, _ *config.Config, target string) source.Source {
			return &source.Homebrew{Client: client, Formula: target}
		},
	},
	{
		Descriptor:   sourceinfo.Must("plausible"),
		missingToken: func(tokens Tokens) string { return missingToken(tokens.Plausible, "PLAUSIBLE_TOKEN not set") },
		metricFactory: func(client *http.Client, tokens Tokens, cfg *config.Config, target string) source.Source {
			return &source.Plausible{Client: client, APIKey: tokens.Plausible, SiteID: target, TimeZone: cfg.Settings.Timezones[target]}
		},
	},
	{
		Descriptor: sourceinfo.Must("openvsx"),
		metricFactory: func(client *http.Client, _ Tokens, _ *config.Config, target string) source.Source {
			return &source.OpenVSX{Client: client, ExtensionID: target}
		},
	},
	{
		Descriptor:   sourceinfo.Must("youtube"),
		missingToken: func(tokens Tokens) string { return missingToken(tokens.YouTube, "YOUTUBE_TOKEN not set") },
		metricFactory: func(client *http.Client, tokens Tokens, _ *config.Config, target string) source.Source {
			return &source.YouTube{Client: client, APIKey: tokens.YouTube, Target: target}
		},
	},
	{
		Descriptor:   sourceinfo.Must("linkedin"),
		missingToken: func(tokens Tokens) string { return missingToken(tokens.LinkedIn, "LINKEDIN_TOKEN not set") },
		beforeFetch: func(ctx context.Context, tokens Tokens) {
			auth.CheckLinkedInTokenExpiry(ctx, tokens.LinkedIn, os.Getenv("LINKEDIN_CLIENT_ID"), os.Getenv("LINKEDIN_CLIENT_SECRET"))
		},
		metricFactory: func(client *http.Client, tokens Tokens, _ *config.Config, target string) source.Source {
			return &source.LinkedIn{Client: client, Token: tokens.LinkedIn, Target: target}
		},
	},
	{
		Descriptor: sourceinfo.Must("rss"),
		metricFactory: func(client *http.Client, _ Tokens, _ *config.Config, target string) source.Source {
			return &source.RSS{Client: client, FeedURL: target}
		},
	},
}

func missingToken(token, reason string) string {
	if token == "" {
		return reason
	}
	return ""
}

func clientForOpts(opts *Options) *http.Client {
	client := &http.Client{Timeout: 30 * time.Second}
	if opts.Stats != nil {
		client = CountingClient(opts.Stats)
	}
	if opts.WrapTransport != nil {
		base := client.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		client.Transport = opts.WrapTransport(base)
	}
	return client
}

func All(ctx context.Context, cfg *config.Config, tokens Tokens, opts Options) ([]Result, error) {
	projects, err := selectedProjects(cfg, opts)
	if err != nil {
		return nil, err
	}

	jobsByKey := make(map[jobKey]*fetchJob)
	var jobOrder []jobKey

	client := clientForOpts(&opts)

	for _, desc := range fetchSourceDescriptors {
		if desc.skipReason(tokens) != "" || (len(opts.Sources) > 0 && !slices.Contains(opts.Sources, desc.Name)) {
			continue
		}
		desc.runBeforeFetch(ctx, tokens)
		for id, proj := range projects {
			desc.addJobs(jobsByKey, &jobOrder, client, tokens, cfg, id, proj)
		}
	}

	jobs := orderedJobs(jobsByKey, jobOrder)
	if opts.Stats != nil {
		opts.Stats.TotalJobs = len(jobs)
	}

	return runJobs(ctx, cfg, opts, jobs, 4)
}

func (d fetchSourceDescriptor) skipReason(tokens Tokens) string {
	if d.missingToken == nil {
		return ""
	}
	return d.missingToken(tokens)
}

func (d fetchSourceDescriptor) runBeforeFetch(ctx context.Context, tokens Tokens) {
	if d.beforeFetch != nil {
		d.beforeFetch(ctx, tokens)
	}
}

func (d fetchSourceDescriptor) addJobs(jobs map[jobKey]*fetchJob, order *[]jobKey, client *http.Client, tokens Tokens, cfg *config.Config, projectID string, proj config.Project) {
	for _, target := range proj.SourceValues(d.Name) {
		if d.eventFactory != nil {
			addEventJob(jobs, order, d.Name, projectID, target, d.eventFactory(client, tokens, cfg, target))
			continue
		}
		addMetricJob(jobs, order, d.Name, projectID, target, d.metricFactory(client, tokens, cfg, target))
	}
}

func fetchSourceByName(name string) (fetchSourceDescriptor, bool) {
	for _, desc := range fetchSourceDescriptors {
		if desc.Name == name {
			return desc, true
		}
	}
	return fetchSourceDescriptor{}, false
}

func runDescriptor(ctx context.Context, cfg *config.Config, tokens Tokens, opts Options, name string) ([]Result, error) {
	desc, ok := fetchSourceByName(name)
	if !ok {
		return nil, fmt.Errorf("unknown source %q", name)
	}
	if reason := desc.skipReason(tokens); reason != "" {
		return []Result{{Source: desc.Name, Skipped: reason}}, nil
	}
	desc.runBeforeFetch(ctx, tokens)

	projects, err := selectedProjects(cfg, opts)
	if err != nil {
		return nil, err
	}

	client := clientForOpts(&opts)
	jobsByKey := make(map[jobKey]*fetchJob)
	var jobOrder []jobKey
	for id, proj := range projects {
		desc.addJobs(jobsByKey, &jobOrder, client, tokens, cfg, id, proj)
	}

	jobs := orderedJobs(jobsByKey, jobOrder)
	if opts.Stats != nil {
		opts.Stats.TotalJobs = len(jobs)
	}

	return runJobs(ctx, cfg, opts, jobs, 1)
}

func SourceByName(ctx context.Context, cfg *config.Config, tokens Tokens, sourceName string, opts Options) ([]Result, error) {
	return runDescriptor(ctx, cfg, tokens, opts, sourceName)
}

func Source(ctx context.Context, cfg *config.Config, _ Tokens, sourceName string, opts Options, createSources func(id string, proj config.Project) []source.Source) ([]Result, error) {
	projects, err := selectedProjects(cfg, opts)
	if err != nil {
		return nil, err
	}

	var jobs []fetchJob

	for id, proj := range projects {
		sources := createSources(id, proj)
		if len(sources) > 0 {
			jobs = append(jobs, fetchJob{sourceName: sourceName, projectID: id, sources: sources, targets: make([]string, len(sources))})
		}
	}

	return runJobs(ctx, cfg, opts, jobs, 1)
}

func GitHub(ctx context.Context, cfg *config.Config, tokens Tokens, opts Options) ([]Result, error) {
	return runDescriptor(ctx, cfg, tokens, opts, "github")
}

func Traffic(ctx context.Context, cfg *config.Config, tokens Tokens, opts Options) ([]Result, error) {
	return runDescriptor(ctx, cfg, tokens, opts, "github-traffic")
}

func PyPI(ctx context.Context, cfg *config.Config, tokens Tokens, opts Options) ([]Result, error) {
	return runDescriptor(ctx, cfg, tokens, opts, "pypi")
}

func CRAN(ctx context.Context, cfg *config.Config, tokens Tokens, opts Options) ([]Result, error) {
	return runDescriptor(ctx, cfg, tokens, opts, "cran")
}

func Homebrew(ctx context.Context, cfg *config.Config, tokens Tokens, opts Options) ([]Result, error) {
	return runDescriptor(ctx, cfg, tokens, opts, "homebrew")
}

func Plausible(ctx context.Context, cfg *config.Config, tokens Tokens, opts Options) ([]Result, error) {
	return runDescriptor(ctx, cfg, tokens, opts, "plausible")
}

func OpenVSX(ctx context.Context, cfg *config.Config, tokens Tokens, opts Options) ([]Result, error) {
	return runDescriptor(ctx, cfg, tokens, opts, "openvsx")
}

func YouTube(ctx context.Context, cfg *config.Config, tokens Tokens, opts Options) ([]Result, error) {
	return runDescriptor(ctx, cfg, tokens, opts, "youtube")
}

func LinkedIn(ctx context.Context, cfg *config.Config, tokens Tokens, opts Options) ([]Result, error) {
	return runDescriptor(ctx, cfg, tokens, opts, "linkedin")
}

func RSS(ctx context.Context, cfg *config.Config, tokens Tokens, opts Options) ([]Result, error) {
	return runDescriptor(ctx, cfg, tokens, opts, "rss")
}

// Reconcilers returns the sources that provide reference totals, for the
// tokens that are set. GitHub's GraphQL API needs a token.
func Reconcilers(cfg *config.Config, tokens Tokens, client *http.Client) map[string]source.Reconciler {
	out := map[string]source.Reconciler{}
	for _, desc := range fetchSourceDescriptors {
		if desc.skipReason(tokens) != "" || (desc.Name == "github" && tokens.GitHub == "") {
			continue
		}
		var src any
		if desc.eventFactory != nil {
			src = desc.eventFactory(client, tokens, cfg, "")
		} else {
			src = desc.metricFactory(client, tokens, cfg, "")
		}
		if r, ok := src.(source.Reconciler); ok {
			out[desc.Name] = r
		}
	}
	return out
}
