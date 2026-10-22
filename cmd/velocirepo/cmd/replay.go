package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/posit-dev/velocirepo/internal/archive"
	"github.com/posit-dev/velocirepo/internal/fetch"
	"github.com/spf13/cobra"
)

func replayCmd() *cobra.Command {
	var archiveDir, sourceName, project, from, to string
	cmd := &cobra.Command{
		Use:   "replay [run-id...]",
		Short: "Rebuild the records of archived runs from their responses",
		Long: `Rebuild the records of archived runs: each job runs again with the options in the run's manifest, against the archived responses instead of the network, and its records are written like fetched ones.

Give the runs by ID, or select every run with --from and --to whose end dates fall in that range, oldest first. Replay repairs bugs in how responses were processed; data that can be fetched again is repaired with fetch instead.`,
		GroupID: "fetch",
		RunE: func(cmd *cobra.Command, args []string) error {
			var runDirs []string
			for _, id := range args {
				runDirs = append(runDirs, filepath.Join(archiveDir, id))
			}
			if from != "" || to != "" {
				if from == "" || to == "" {
					return fmt.Errorf("--from and --to go together")
				}
				found, err := archive.RunsInRange(archiveDir, sourceName, project, from, to)
				if err != nil {
					return err
				}
				runDirs = append(runDirs, found...)
			}
			if len(runDirs) == 0 {
				return fmt.Errorf("give run IDs, or --from and --to")
			}

			var all []fetch.Result
			for _, dir := range runDirs {
				run, err := archive.ReadRun(dir)
				if err != nil {
					return err
				}
				results, err := fetch.Replay(cmd.Context(), cfg, run, fetch.ReplayOptions{Source: sourceName, Project: project})
				if err != nil {
					return err
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Replayed %s: %d job(s)\n", run.Manifest.RunID, len(results))
				all = append(all, results...)
			}
			renderFetchResults(all)
			writeFetchLog(all, "replay")
			if len(all) > 0 {
				rebuildDB()
			}
			for _, r := range all {
				if r.Error != "" {
					return fmt.Errorf("some jobs couldn't be replayed")
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&archiveDir, "archive", "", "the directory with the archived runs (required)")
	cmd.Flags().StringVar(&sourceName, "source", "", "replay only this source")
	cmd.Flags().StringVar(&project, "project", "", "replay only this project")
	cmd.Flags().StringVar(&from, "from", "", "replay the runs that fetched through this day or later (YYYY-MM-DD)")
	cmd.Flags().StringVar(&to, "to", "", "replay the runs that fetched through this day or earlier (YYYY-MM-DD)")
	_ = cmd.MarkFlagRequired("archive")
	return cmd
}
