package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync/atomic"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/fetch"
	"github.com/posit-dev/velocirepo/internal/ui"
	"github.com/spf13/cobra"
)

var (
	fetchProject   string
	fetchStartDate string
	fetchEndDate   string
	noConcatenate  bool
)

func addFetchFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&fetchProject, "project", "", "fetch only this project ID")
	cmd.Flags().StringVar(&fetchStartDate, "start-date", "", "start date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&fetchEndDate, "end-date", "", "end date (YYYY-MM-DD, default: yesterday)")
	cmd.Flags().BoolVar(&noConcatenate, "no-concatenate", false, "skip concatenation after fetch")
	cmd.GroupID = "fetch"
}

func fetchOpts() fetch.Options {
	stats := fetch.NewStats(0)
	var completed atomic.Int32
	return fetch.Options{
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

type fetchSourceDef struct {
	use   string
	short string
	fn    func(context.Context, *config.Config, fetch.Tokens, fetch.Options) ([]fetch.Result, error)
}

var fetchSources = []fetchSourceDef{
	{"fetch-cran", "Fetch CRAN download statistics", fetch.CRAN},
	{"fetch-github", "Fetch GitHub events (stars, forks, issues, PRs)", fetch.GitHub},
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
			if err != nil {
				return err
			}
			renderFetchResults(results)

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

			rebuildDB()
			return nil
		},
	}
	addFetchFlags(cmd)
	return cmd
}
