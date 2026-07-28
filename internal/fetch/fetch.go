package fetch

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
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
	Project       string
	StartDate     string
	EndDate       string
	NoConcatenate bool
	OnResult      ResultCallback
}

type Result struct {
	Source    string        `json:"source"`
	ProjectID string        `json:"project_id"`
	Records   int           `json:"records"`
	StartDate string        `json:"start_date"`
	EndDate   string        `json:"end_date"`
	Duration  time.Duration `json:"duration,omitempty"`
	Skipped   string        `json:"skipped,omitempty"`
	Error     string        `json:"error,omitempty"`
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

func resolveEndDate(cfg *config.Config, endDateStr string) (time.Time, error) {
	if endDateStr != "" {
		return dateutil.ParseDate(endDateStr)
	}
	if cfg.Settings.EndDate == "yesterday" || cfg.Settings.EndDate == "" {
		return time.Now().UTC().AddDate(0, 0, -1).Truncate(24 * time.Hour), nil
	}
	return dateutil.ParseDate(cfg.Settings.EndDate)
}

func resolveStartDate(dataDir, sourceName, projectID, startDateStr string) (time.Time, error) {
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
		earliest := time.Now().UTC().AddDate(0, 0, -14).Truncate(24 * time.Hour)
		if start.Before(earliest) {
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
	sourceName   string
	projectID    string
	sources      []source.Source
	eventSources []source.EventSource
	startDate    time.Time
	startErr     error
}

type jobKey struct {
	sourceName string
	projectID  string
}

func addMetricJob(jobs map[jobKey]*fetchJob, order *[]jobKey, sourceName, projectID string, src source.Source) {
	key := jobKey{sourceName: sourceName, projectID: projectID}
	job := jobs[key]
	if job == nil {
		job = &fetchJob{sourceName: sourceName, projectID: projectID}
		jobs[key] = job
		*order = append(*order, key)
	}
	job.sources = append(job.sources, src)
}

func addEventJob(jobs map[jobKey]*fetchJob, order *[]jobKey, sourceName, projectID string, src source.EventSource) {
	key := jobKey{sourceName: sourceName, projectID: projectID}
	job := jobs[key]
	if job == nil {
		job = &fetchJob{sourceName: sourceName, projectID: projectID}
		jobs[key] = job
		*order = append(*order, key)
	}
	job.eventSources = append(job.eventSources, src)
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
	endDate, err := resolveEndDate(cfg, opts.EndDate)
	if err != nil {
		return nil, fmt.Errorf("parse end date: %w", err)
	}

	dataDir := cfg.DataDir()
	for i := range jobs {
		startDate, err := resolveStartDate(dataDir, jobs[i].sourceName, jobs[i].projectID, opts.StartDate)
		jobs[i].startDate = startDate
		jobs[i].startErr = err
	}

	if concurrency <= 0 {
		concurrency = 1
	}

	resultsCh := make(chan []Result, len(jobs))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)

	for _, job := range jobs {
		job := job
		g.Go(func() error {
			resultsCh <- runJob(gctx, dataDir, endDate, job)
			return nil
		})
	}

	var results []Result
	done := make(chan struct{})
	go func() {
		for jobResults := range resultsCh {
			results = append(results, jobResults...)
			if onResult != nil {
				for _, r := range jobResults {
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
		if err := store.Aggregate(dataDir, time.Now().UTC()); err != nil {
			return results, fmt.Errorf("concatenation: %w", err)
		}
	}

	return results, nil
}

func runJob(ctx context.Context, dataDir string, endDate time.Time, job fetchJob) []Result {
	started := time.Now()
	if job.startErr != nil {
		return []Result{{
			Source:    job.sourceName,
			ProjectID: job.projectID,
			Duration:  time.Since(started),
			Error:     fmt.Sprintf("resolve start date: %v", job.startErr),
		}}
	}

	if !job.startDate.Before(endDate.AddDate(0, 0, 1)) {
		return []Result{{
			Source:    job.sourceName,
			ProjectID: job.projectID,
			Duration:  time.Since(started),
			Skipped:   "already up to date",
		}}
	}

	if len(job.eventSources) > 0 {
		return runEventJob(ctx, dataDir, job, source.FetchOptions{
			ProjectID: job.projectID,
			StartDate: job.startDate,
			EndDate:   endDate,
			DataDir:   dataDir,
		}, started)
	}

	if len(job.sources) == 0 {
		return []Result{{
			Source:    job.sourceName,
			ProjectID: job.projectID,
			Duration:  time.Since(started),
			Error:     "no fetcher configured",
		}}
	}

	return runMetricJob(ctx, dataDir, job, source.FetchOptions{
		ProjectID: job.projectID,
		StartDate: job.startDate,
		EndDate:   endDate,
		DataDir:   dataDir,
	}, started)
}

func runEventJob(ctx context.Context, dataDir string, job fetchJob, opts source.FetchOptions, started time.Time) []Result {
	var results []Result
	var events []source.Event
	contentByFilename := make(map[string][]source.ContentEntry)

	var records []source.Record
	for _, eventSrc := range job.eventSources {
		fetched, err := eventSrc.FetchEvents(ctx, opts)
		if err != nil {
			results = append(results, Result{
				Source:    job.sourceName,
				ProjectID: job.projectID,
				Duration:  time.Since(started),
				Error:     err.Error(),
			})
			continue
		}
		events = append(events, fetched...)
		if mcp, ok := eventSrc.(source.MultiContentProvider); ok {
			for filename, entries := range mcp.ContentByFilename() {
				contentByFilename[filename] = append(contentByFilename[filename], entries...)
			}
		}
		if rp, ok := eventSrc.(source.RecordProvider); ok {
			records = append(records, rp.Records()...)
		}
	}

	if len(results) > 0 {
		return results
	}

	if len(events) == 0 && len(contentByFilename) == 0 && len(records) == 0 {
		return []Result{{
			Source:    job.sourceName,
			ProjectID: job.projectID,
			Duration:  time.Since(started),
			Skipped:   "no new events",
		}}
	}

	if len(events) > 0 {
		if err := store.WriteEvents(dataDir, job.sourceName, job.projectID, events); err != nil {
			return append(results, Result{
				Source:    job.sourceName,
				ProjectID: job.projectID,
				Duration:  time.Since(started),
				Error:     fmt.Sprintf("write: %v", err),
			})
		}
	}

	for filename, entries := range contentByFilename {
		if err := store.WriteContent(dataDir, job.sourceName, job.projectID, filename, entries); err != nil {
			slog.Warn("write content failed", "source", job.sourceName, "project", job.projectID, "error", err)
		}
	}

	if len(records) > 0 {
		if err := store.WriteRecords(dataDir, job.sourceName, job.projectID, records); err != nil {
			slog.Warn("write records failed", "source", job.sourceName, "project", job.projectID, "error", err)
		}
	}

	return append(results, Result{
		Source:    job.sourceName,
		ProjectID: job.projectID,
		Records:   len(events) + len(records),
		StartDate: dateutil.FormatDate(opts.StartDate),
		EndDate:   dateutil.FormatDate(opts.EndDate),
		Duration:  time.Since(started),
	})
}

func runMetricJob(ctx context.Context, dataDir string, job fetchJob, opts source.FetchOptions, started time.Time) []Result {
	var results []Result
	var records []source.Record
	contentByFilename := make(map[string][]source.ContentEntry)

	for _, src := range job.sources {
		fetched, err := src.Fetch(ctx, opts)
		if err != nil {
			results = append(results, Result{
				Source:    job.sourceName,
				ProjectID: job.projectID,
				Duration:  time.Since(started),
				Error:     err.Error(),
			})
			continue
		}
		if len(fetched) == 0 {
			continue
		}
		records = append(records, fetched...)
		if cp, ok := src.(source.ContentProvider); ok {
			if entries := cp.ContentEntries(); len(entries) > 0 {
				contentByFilename[cp.ContentFilename()] = append(contentByFilename[cp.ContentFilename()], entries...)
			}
		}
	}

	if len(results) > 0 {
		return results
	}

	if len(records) == 0 {
		return []Result{{
			Source:    job.sourceName,
			ProjectID: job.projectID,
			Duration:  time.Since(started),
			Skipped:   "no new records",
		}}
	}

	if err := store.WriteRecords(dataDir, job.sourceName, job.projectID, records); err != nil {
		return append(results, Result{
			Source:    job.sourceName,
			ProjectID: job.projectID,
			Duration:  time.Since(started),
			Error:     fmt.Sprintf("write: %v", err),
		})
	}

	for filename, entries := range contentByFilename {
		if err := store.WriteContent(dataDir, job.sourceName, job.projectID, filename, entries); err != nil {
			slog.Warn("write content failed", "source", job.sourceName, "project", job.projectID, "error", err)
		}
	}

	return append(results, Result{
		Source:    job.sourceName,
		ProjectID: job.projectID,
		Records:   len(records),
		StartDate: dateutil.FormatDate(opts.StartDate),
		EndDate:   dateutil.FormatDate(opts.EndDate),
		Duration:  time.Since(started),
	})
}

type fetchSourceDescriptor struct {
	sourceinfo.Descriptor
	missingToken  func(Tokens) string
	beforeFetch   func(context.Context, Tokens)
	metricFactory func(*http.Client, Tokens, string) source.Source
	eventFactory  func(*http.Client, Tokens, string) source.EventSource
}

var fetchSourceDescriptors = []fetchSourceDescriptor{
	{
		Descriptor: sourceinfo.Must("github-traffic"),
		metricFactory: func(client *http.Client, tokens Tokens, target string) source.Source {
			return &source.GitHubTraffic{Client: client, Token: tokens.GitHub, Repo: target}
		},
	},
	{
		Descriptor: sourceinfo.Must("github"),
		eventFactory: func(client *http.Client, tokens Tokens, target string) source.EventSource {
			return &source.GitHubEvents{Client: client, Token: tokens.GitHub, Repo: target}
		},
	},
	{
		Descriptor: sourceinfo.Must("pypi"),
		metricFactory: func(client *http.Client, _ Tokens, target string) source.Source {
			return &source.PyPI{Client: client, Package: target}
		},
	},
	{
		Descriptor: sourceinfo.Must("cran"),
		metricFactory: func(client *http.Client, _ Tokens, target string) source.Source {
			return &source.CRAN{Client: client, Package: target}
		},
	},
	{
		Descriptor: sourceinfo.Must("homebrew"),
		metricFactory: func(client *http.Client, _ Tokens, target string) source.Source {
			return &source.Homebrew{Client: client, Formula: target}
		},
	},
	{
		Descriptor:   sourceinfo.Must("plausible"),
		missingToken: func(tokens Tokens) string { return missingToken(tokens.Plausible, "PLAUSIBLE_TOKEN not set") },
		metricFactory: func(client *http.Client, tokens Tokens, target string) source.Source {
			return &source.Plausible{Client: client, APIKey: tokens.Plausible, SiteID: target}
		},
	},
	{
		Descriptor: sourceinfo.Must("openvsx"),
		metricFactory: func(client *http.Client, _ Tokens, target string) source.Source {
			return &source.OpenVSX{Client: client, ExtensionID: target}
		},
	},
	{
		Descriptor:   sourceinfo.Must("youtube"),
		missingToken: func(tokens Tokens) string { return missingToken(tokens.YouTube, "YOUTUBE_TOKEN not set") },
		metricFactory: func(client *http.Client, tokens Tokens, target string) source.Source {
			return &source.YouTube{Client: client, APIKey: tokens.YouTube, Target: target}
		},
	},
	{
		Descriptor:   sourceinfo.Must("linkedin"),
		missingToken: func(tokens Tokens) string { return missingToken(tokens.LinkedIn, "LINKEDIN_TOKEN not set") },
		beforeFetch: func(ctx context.Context, tokens Tokens) {
			auth.CheckLinkedInTokenExpiry(ctx, tokens.LinkedIn, os.Getenv("LINKEDIN_CLIENT_ID"), os.Getenv("LINKEDIN_CLIENT_SECRET"))
		},
		metricFactory: func(client *http.Client, tokens Tokens, target string) source.Source {
			return &source.LinkedIn{Client: client, Token: tokens.LinkedIn, Target: target}
		},
	},
	{
		Descriptor: sourceinfo.Must("rss"),
		metricFactory: func(client *http.Client, _ Tokens, target string) source.Source {
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

func All(ctx context.Context, cfg *config.Config, tokens Tokens, opts Options) ([]Result, error) {
	projects, err := selectedProjects(cfg, opts)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 30 * time.Second}
	jobsByKey := make(map[jobKey]*fetchJob)
	var jobOrder []jobKey

	for _, desc := range fetchSourceDescriptors {
		if desc.skipReason(tokens) != "" {
			continue
		}
		desc.runBeforeFetch(ctx, tokens)
		for id, proj := range projects {
			desc.addJobs(jobsByKey, &jobOrder, client, tokens, id, proj)
		}
	}

	return runJobs(ctx, cfg, opts, orderedJobs(jobsByKey, jobOrder), 4)
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

func (d fetchSourceDescriptor) addJobs(jobs map[jobKey]*fetchJob, order *[]jobKey, client *http.Client, tokens Tokens, projectID string, proj config.Project) {
	for _, target := range proj.SourceValues(d.Name) {
		if d.eventFactory != nil {
			addEventJob(jobs, order, d.Name, projectID, d.eventFactory(client, tokens, target))
			continue
		}
		addMetricJob(jobs, order, d.Name, projectID, d.metricFactory(client, tokens, target))
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

	client := &http.Client{Timeout: 30 * time.Second}
	jobsByKey := make(map[jobKey]*fetchJob)
	var jobOrder []jobKey
	for id, proj := range projects {
		desc.addJobs(jobsByKey, &jobOrder, client, tokens, id, proj)
	}

	return runJobs(ctx, cfg, opts, orderedJobs(jobsByKey, jobOrder), 1)
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
			jobs = append(jobs, fetchJob{sourceName: sourceName, projectID: id, sources: sources})
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
