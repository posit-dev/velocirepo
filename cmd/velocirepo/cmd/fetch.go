package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/posit-dev/velocirepo/internal/archive"
	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/fetch"
	"github.com/posit-dev/velocirepo/internal/store"
	"github.com/posit-dev/velocirepo/internal/ui"
	"github.com/posit-dev/velocirepo/internal/version"
	"github.com/spf13/cobra"
)

var (
	fetchProject   string
	fetchStartDate string
	fetchEndDate   string
	noConcatenate  bool
	fetchResults   string
	fetchArchive   string

	// activeArchive is the archive of the running fetch, if any.
	activeArchive *archive.Writer
)

func addFetchFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&fetchProject, "project", "", "fetch only this project ID")
	cmd.Flags().StringVar(&fetchStartDate, "start-date", "", "start date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&fetchEndDate, "end-date", "", "end date (YYYY-MM-DD, default: yesterday)")
	cmd.Flags().BoolVar(&noConcatenate, "no-concatenate", false, "skip concatenation after fetch")
	cmd.Flags().StringVar(&fetchResults, "results", "", "write what every job did to this JSON file")
	cmd.Flags().StringVar(&fetchArchive, "archive", "", "archive the responses of data that can't be fetched again in this directory")
	cmd.GroupID = "fetch"
}

func fetchOpts() fetch.Options {
	stats := fetch.NewStats(0)
	var completed atomic.Int32
	opts := fetch.Options{
		Project:       fetchProject,
		StartDate:     fetchStartDate,
		EndDate:       fetchEndDate,
		NoConcatenate: noConcatenate,
		Quiet:         quiet,
		Stats:         stats,
		OnResult: func(r fetch.Result) {
			n := int(completed.Add(1))
			renderResultWithProgress(r, n, stats.TotalJobs)
		},
	}
	activeArchive = nil
	if fetchArchive != "" && cfg.Settings.ArchiveEnabled() {
		now := time.Now()
		w, err := archive.NewWriter(fetchArchive, archive.NewRunID(now, os.Getenv), version.Version, now)
		if err != nil {
			slog.Warn("archive", "error", err)
		} else {
			fetch.AttachArchive(&opts, w)
			activeArchive = w
		}
	}
	return opts
}

// closeArchive finishes the run's archive. An archive that contains one of
// the tokens is deleted: it would leak the token wherever it's uploaded.
func closeArchive() {
	if activeArchive == nil {
		return
	}
	w := activeArchive
	activeArchive = nil
	if _, err := w.Close(); err != nil {
		slog.Warn("close archive", "error", err)
	}
	tokens := fetch.TokensFromEnv()
	found, err := archive.FindSecrets(w.Dir(), []string{tokens.GitHub, tokens.Plausible, tokens.YouTube, tokens.LinkedIn})
	if err != nil {
		slog.Warn("check archive for secrets", "error", err)
		return
	}
	if len(found) > 0 {
		_ = os.RemoveAll(w.Dir())
		ui.Errorf("the archive contained a token (in %s), so it was deleted", strings.Join(found, ", "))
		return
	}
	ui.Infof("Archived responses in %s", w.Dir())
}

func renderResultWithProgress(r fetch.Result, completed, total int) {
	switch {
	case r.Error != "":
		ui.FetchProgress(completed, total, "✗", r.Source, r.ProjectID, r.Error, "\033[31m")
	case r.Skipped != "":
		ui.FetchProgress(completed, total, "·", r.Source, r.ProjectID, r.Skipped, "\033[2m")
	default:
		detail := fmt.Sprintf("%d records  %s", r.Records, fmtDuration(r.Duration))
		ui.FetchProgress(completed, total, "✓", r.Source, r.ProjectID, detail, "\033[32m")
	}
}

func fmtDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func renderFetchResults(results []fetch.Result) {
	var failures []fetch.Result
	for _, r := range results {
		if r.Error != "" {
			failures = append(failures, r)
		}
	}
	if len(failures) == 0 {
		return
	}

	sort.Slice(failures, func(i, j int) bool {
		if failures[i].Source != failures[j].Source {
			return failures[i].Source < failures[j].Source
		}
		return failures[i].ProjectID < failures[j].ProjectID
	})

	fmt.Fprintln(os.Stderr)
	ui.Warnf("%d fetch(es) failed:", len(failures))
	for _, f := range failures {
		ui.Errorf("%s %s", ui.Prefix(f.Source, f.ProjectID), f.Error)
	}
}

// finishFetch reports a fetch run and returns the error the command exits
// with. Whatever was fetched is logged, summarised and loaded into the
// database first, so a partial failure keeps the data of the jobs that
// succeeded while the exit code still shows that something went wrong.
func finishFetch(opts fetch.Options, results []fetch.Result, fetchErr error, command string) error {
	renderFetchResults(results)
	writeFetchLog(results, command)
	closeArchive()
	if fetchResults != "" {
		if err := fetch.WriteResults(fetchResults, results, time.Now()); err != nil {
			slog.Warn("write results", "error", err)
		}
	}

	if opts.Stats != nil {
		ui.FetchSummary(ui.FetchStats{
			Elapsed:      opts.Stats.Elapsed(),
			Records:      opts.Stats.Records,
			FilesWritten: opts.Stats.FilesWritten,
			APICalls:     opts.Stats.APICalls,
			Succeeded:    opts.Stats.Succeeded,
			Skipped:      opts.Stats.Skipped,
			Failed:       opts.Stats.Failed,
		})
	}

	if len(results) > 0 {
		rebuildDB()
	}

	if fetchErr != nil {
		return fetchErr
	}
	failed := 0
	for _, r := range results {
		if r.Error != "" {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d fetch job(s) failed", failed)
	}
	return nil
}

func writeFetchLog(results []fetch.Result, command string) {
	if len(results) == 0 {
		return
	}

	now := time.Now().UTC()
	runID := now.Format(time.RFC3339)

	entries := make([]store.LogEntry, len(results))
	for i, r := range results {
		entries[i] = store.LogEntry{
			RunID:      runID,
			Timestamp:  now.Format(time.RFC3339),
			Version:    version.Version,
			Command:    command,
			Source:     r.Source,
			ProjectID:  r.ProjectID,
			Records:    r.Records,
			Files:      r.Files,
			StartDate:  r.StartDate,
			EndDate:    r.EndDate,
			DurationMS: r.Duration.Milliseconds(),
			StatusCode: r.StatusCode,
			Skipped:    r.Skipped,
			Error:      r.Error,
		}
	}

	if err := store.WriteLog(cfg.DataDir(), entries); err != nil {
		slog.Warn("write fetch log", "error", err)
	}
}

type fetchSourceDef struct {
	use   string
	short string
	fn    func(context.Context, *config.Config, fetch.Tokens, fetch.Options) ([]fetch.Result, error)
}

var fetchSources = []fetchSourceDef{
	{"fetch-cran", "Fetch CRAN download statistics", fetch.CRAN},
	{"fetch-github", "Fetch GitHub events (forks, issues, PRs, comments) and stars", fetch.GitHub},
	{"fetch-traffic", "Fetch GitHub traffic data (views and clones)", fetch.Traffic},
	{"fetch-homebrew", "Fetch Homebrew install counts", fetch.Homebrew},
	{"fetch-openvsx", "Fetch Open VSX extension metrics", fetch.OpenVSX},
	{"fetch-plausible", "Fetch Plausible analytics (pageviews, visitors, visits)", fetch.Plausible},
	{"fetch-pypi", "Fetch PyPI download statistics", fetch.PyPI},
	{"fetch-youtube", "Fetch YouTube metrics (views, likes, comments, subscribers)", fetch.YouTube},
	{"fetch-linkedin", "Fetch LinkedIn post metrics and content", fetch.LinkedIn},
	{"fetch-rss", "Fetch items from RSS or Atom feeds as content entries", fetch.RSS},
}

func makeFetchCmd(def fetchSourceDef) *cobra.Command {
	cmd := &cobra.Command{
		Use:   def.use,
		Short: def.short,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := fetchOpts()

			projects := len(cfg.Projects)
			if fetchProject != "" {
				projects = 1
			}
			ui.FetchPlan(1, projects, projects)

			results, err := def.fn(cmd.Context(), cfg, fetch.TokensFromEnv(), opts)
			return finishFetch(opts, results, err, def.use)
		},
	}
	addFetchFlags(cmd)
	return cmd
}
