package cmd

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/posit-dev/velocirepo/internal/fetch"
	"github.com/posit-dev/velocirepo/internal/gate"
	"github.com/posit-dev/velocirepo/internal/ui"
	"github.com/spf13/cobra"
)

func gateCmd() *cobra.Command {
	var repo repoFlags
	var archiveDir, release, discard, repair string
	var noConcatenate, noReconcile bool
	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Run the nightly fetch so that it only adds data",
		Long: `Run the nightly fetch in GitHub Actions: check the commits made since the last run, fetch, check the night's changes, commit the data, concatenate on or after the concatenation day in a commit of its own, push, and report in the tracking issue labelled velocirepo-health.

This version only reports what an enforcing gate would restore, keep and hold back; it commits everything, as fetch does.

GITHUB_TOKEN is the token velocirepo fetches with. The tracking issue and the archive use the workflow token, from --repo-token or VELOCIREPO_REPO_TOKEN.`,
		GroupID: "ci",
		RunE: func(cmd *cobra.Command, args []string) error {
			for name, value := range map[string]string{"release": release, "discard": discard, "repair": repair} {
				if value != "" {
					ui.Warnf("--%s is available from velocirepo v1.4.0; this run only reports", name)
				}
			}
			repoName, repoToken, err := repo.resolve()
			if err != nil {
				ui.Warnf("%v; there's no tracking issue and no archive upload", err)
			}
			if archiveDir == "" && os.Getenv("RUNNER_TEMP") != "" {
				archiveDir = filepath.Join(os.Getenv("RUNNER_TEMP"), "velocirepo-archive")
			}
			tokens := fetch.TokensFromEnv()
			opts := gate.Options{
				Config: cfg, Tokens: tokens, Repo: repoName, RepoToken: repoToken,
				ArchiveDir: archiveDir, NoConcatenate: noConcatenate,
				OutputPath: os.Getenv("GITHUB_OUTPUT"), SummaryPath: os.Getenv("GITHUB_STEP_SUMMARY"),
				Log: cmd.OutOrStdout(),
				AfterFetch: func(results []fetch.Result) {
					renderFetchResults(results)
					writeFetchLog(results, "gate")
				},
				BuildDB: rebuildDB,
			}
			if server, id := os.Getenv("GITHUB_SERVER_URL"), os.Getenv("GITHUB_RUN_ID"); server != "" && id != "" {
				opts.RunURL = fmt.Sprintf("%s/%s/actions/runs/%s", server, repoName, id)
			}
			if os.Getenv("GITHUB_ACTIONS") == "true" {
				opts.GitEnv = []string{
					"GIT_AUTHOR_NAME=github-actions[bot]", "GIT_AUTHOR_EMAIL=41898282+github-actions[bot]@users.noreply.github.com",
					"GIT_COMMITTER_NAME=github-actions[bot]", "GIT_COMMITTER_EMAIL=41898282+github-actions[bot]@users.noreply.github.com",
				}
			}
			if !noReconcile {
				opts.Reconcilers = fetch.Reconcilers(cfg, tokens, &http.Client{Timeout: time.Minute})
			}

			out, err := gate.Run(cmd.Context(), opts)
			if err != nil {
				return err
			}
			ui.Infof("%d finding(s) open: %d new, %d resolved", len(out.Changes.Open), len(out.Changes.New), len(out.Changes.Resolved))
			if out.Failed() {
				return fmt.Errorf("the gate has open errors; see the tracking issue")
			}
			return nil
		},
	}
	repo.add(cmd)
	cmd.Flags().StringVar(&archiveDir, "archive", "", "archive the responses in this directory (default: $RUNNER_TEMP/velocirepo-archive)")
	cmd.Flags().BoolVar(&noConcatenate, "no-concatenate", false, "don't concatenate")
	cmd.Flags().BoolVar(&noReconcile, "no-reconcile", false, "don't compare stored totals with the APIs' own totals")
	// The workflow's manual inputs come through the environment, since they
	// contain spaces.
	cmd.Flags().StringVar(&release, "release", os.Getenv("VELOCIREPO_RELEASE"), "land a project's held-back data: <source>/<project> (from v1.4.0; default: $VELOCIREPO_RELEASE)")
	cmd.Flags().StringVar(&discard, "discard", os.Getenv("VELOCIREPO_DISCARD"), "discard a project's held-back data: <source>/<project> (from v1.4.0; default: $VELOCIREPO_DISCARD)")
	cmd.Flags().StringVar(&repair, "repair", os.Getenv("VELOCIREPO_REPAIR"), "re-fetch or replay a range: <source>/<project> <from>..<to> (from v1.4.0; default: $VELOCIREPO_REPAIR)")
	return cmd
}
