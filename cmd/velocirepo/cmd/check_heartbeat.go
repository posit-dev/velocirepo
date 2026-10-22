package cmd

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/posit-dev/velocirepo/internal/gate"
	"github.com/posit-dev/velocirepo/internal/ui"
	"github.com/spf13/cobra"
)

func checkHeartbeatCmd() *cobra.Command {
	var repo repoFlags
	var workflow, workflowsDir string
	cmd := &cobra.Command{
		Use:   "check-heartbeat",
		Short: "Check that the nightly workflow ran, and report it when it didn't",
		Long: `Check with GitHub's Actions API that the nightly workflow started its scheduled run since it was last due; a run in progress counts, manual runs don't. When it didn't, comment on the tracking issue, or open it, and fail. After the issue has been open for three days, it escalates once.

The schedule is read from the workflow file. Uses the workflow token, from --repo-token or VELOCIREPO_REPO_TOKEN, with issues: write and actions: read.`,
		GroupID: "ci",
		RunE: func(cmd *cobra.Command, args []string) error {
			repoName, token, err := repo.resolve()
			if err != nil {
				return err
			}
			cron, err := gate.WorkflowCron(filepath.Join(workflowsDir, workflow))
			if err != nil {
				return err
			}
			res, err := gate.NewHeartbeat(repoName, token, "", workflow, cron, time.Now()).Check(cmd.Context())
			if err != nil {
				return err
			}
			if !res.Ran {
				return fmt.Errorf("%s didn't start its scheduled run of %s UTC", workflow, res.Scheduled.Format("2006-01-02 15:04"))
			}
			ui.Infof("%s started its scheduled run of %s UTC", workflow, res.Scheduled.Format("2006-01-02 15:04"))
			return nil
		},
	}
	setCommandAnnotation(cmd, requiresConfigAnnotation, "false")
	repo.add(cmd)
	cmd.Flags().StringVar(&workflow, "workflow", "velocirepo.yml", "the nightly workflow's file name")
	cmd.Flags().StringVar(&workflowsDir, "workflows-dir", ".github/workflows", "where the workflow files are")
	return cmd
}
