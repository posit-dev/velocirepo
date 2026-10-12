package cmd

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/spf13/cobra"
)

func validateProjectsCmd() *cobra.Command {
	var projectFilter string
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:     "validate-projects",
		Short:   "Verify that configured sources are reachable",
		GroupID: "project",
		RunE: func(cmd *cobra.Command, args []string) error {
			projects := cfg.Projects
			if len(projects) == 0 {
				return fmt.Errorf("no projects configured")
			}

			if projectFilter != "" {
				p, err := cfg.GetProject(projectFilter)
				if err != nil {
					return err
				}
				projects = map[string]config.Project{projectFilter: p}
			}

			token := os.Getenv("GITHUB_TOKEN")
			if token == "" {
				for _, p := range projects {
					if !p.GitHubEvents.IsEmpty() || !p.GitHubTraffic.IsEmpty() {
						fmt.Fprintln(os.Stderr, "Warning: GITHUB_TOKEN not set (private repos will fail)")
						break
					}
				}
			}

			client := &http.Client{Timeout: timeout}
			opts := config.ValidationOptions{
				Client:  client,
				Timeout: timeout,
				Token:   token,
			}

			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			var totalSources, passed, failed int

			for id, proj := range projects {
				_, _ = fmt.Fprintf(out, "%s\n", id)
				results := config.ValidateProject(ctx, opts, id, proj)
				for _, r := range results {
					totalSources++
					if r.OK {
						passed++
						_, _ = fmt.Fprintf(out, "  ✓ %-12s %s\n", r.Source, r.Value)
					} else {
						failed++
						_, _ = fmt.Fprintf(out, "  ✗ %-12s %s — %s\n", r.Source, r.Value, r.Error)
					}
				}
				_, _ = fmt.Fprintln(out)
			}

			_, _ = fmt.Fprintf(out, "%d projects, %d sources: %d passed, %d failed\n",
				len(projects), totalSources, passed, failed)

			if failed > 0 {
				return fmt.Errorf("%d source(s) failed validation", failed)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&projectFilter, "project", "", "validate only this project")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "per-source HTTP timeout")

	return cmd
}
