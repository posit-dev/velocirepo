package cmd

import (
	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/fetch"
	"github.com/posit-dev/velocirepo/internal/ui"
	"github.com/spf13/cobra"
)

func fetchAllCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fetch",
		Short: "Fetch from all configured sources",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := fetchOpts()

			sources := countConfiguredSources(cfg)
			projects := len(cfg.Projects)
			ui.FetchPlan(sources, projects, sources*projects)

			results, err := fetch.All(cmd.Context(), cfg, fetch.TokensFromEnv(), opts)
			return finishFetch(opts, results, err, "fetch")
		},
	}

	addFetchFlags(cmd)
	return cmd
}

func countConfiguredSources(c *config.Config) int {
	seen := make(map[string]bool)
	for _, proj := range c.Projects {
		for _, name := range proj.SourceNames() {
			seen[name] = true
		}
	}
	return len(seen)
}
