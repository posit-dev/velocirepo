package cmd

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/posit-dev/velocirepo/internal/gate"
	"github.com/spf13/cobra"
)

func digestCmd() *cobra.Command {
	var repo repoFlags
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "digest",
		Short: "Post a weekly digest of the data on its own issue",
		Long: `Render a sparkline per source and project over the last 8 weeks, with the week's findings, and post it as a comment on the long-lived "Weekly digest" issue labelled velocirepo-digest, for the problems no check was written for. It never opens or closes the tracking issue.

Uses the workflow token, from --repo-token or VELOCIREPO_REPO_TOKEN. With --dry-run, the digest is printed instead.`,
		GroupID: "ci",
		RunE: func(cmd *cobra.Command, args []string) error {
			now := time.Now()
			rows, start, err := gate.DigestRows(cfg.DataDir(), now)
			if err != nil {
				return err
			}
			state, err := gate.ReadState(filepath.Join(filepath.Dir(cfg.DataDir()), gate.StateFile))
			if err != nil {
				return err
			}
			body := gate.RenderDigest(rows, start, state, now)
			if dryRun {
				_, err := fmt.Fprint(cmd.OutOrStdout(), body)
				return err
			}
			repoName, token, err := repo.resolve()
			if err != nil {
				return err
			}
			return gate.PostDigest(cmd.Context(), gate.NewDigestAPI(repoName, token, ""), body)
		},
	}
	repo.add(cmd)
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the digest instead of posting it")
	return cmd
}
