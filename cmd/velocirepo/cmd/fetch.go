package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"

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
	return fetch.Options{
		Project:       fetchProject,
		StartDate:     fetchStartDate,
		EndDate:       fetchEndDate,
		NoConcatenate: noConcatenate,
		OnResult:      renderResult,
	}
}

func renderResult(r fetch.Result) {
	switch {
	case r.Error != "":
		ui.FetchError(r.Source, r.ProjectID, fmt.Errorf("%s", r.Error))
	case r.Skipped != "":
		ui.FetchSkip(r.Source, r.ProjectID, r.Skipped)
	default:
		ui.FetchDone(r.Source, r.ProjectID, r.Records, r.Duration)
	}
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
			results, err := def.fn(cmd.Context(), cfg, fetch.TokensFromEnv(), fetchOpts())
			if err != nil {
				return err
			}
			renderFetchResults(results)
			rebuildDB()
			return nil
		},
	}
	addFetchFlags(cmd)
	return cmd
}
