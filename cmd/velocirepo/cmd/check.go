package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/posit-dev/velocirepo/internal/check"
	"github.com/posit-dev/velocirepo/internal/fetch"
	"github.com/posit-dev/velocirepo/internal/store"
	"github.com/spf13/cobra"
)

// checksFile is velocirepo/checks.toml, next to the data directory, where
// accepted findings are kept.
func checksFile() string {
	return filepath.Join(filepath.Dir(cfg.DataDir()), "checks.toml")
}

func checkCmd() *cobra.Command {
	var resultsPath, fetchLog, jsonPath, summaryPath string
	cmd := &cobra.Command{
		Use:     "check",
		Short:   "Check stored data and a fetch run for problems",
		Long:    "Run velocirepo's checks and report their findings. Exits non-zero when there are errors that aren't accepted in velocirepo/checks.toml.",
		GroupID: "data",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := &check.Context{Config: cfg, DataDir: cfg.DataDir(), Now: time.Now()}
			switch {
			case resultsPath != "":
				r, err := fetch.ReadResults(resultsPath)
				if err != nil {
					return err
				}
				c.Results = &r
			case fetchLog != "":
				entries, err := store.ReadLog(cfg.DataDir(), fetchLog)
				if err != nil {
					return fmt.Errorf("read fetch log: %w", err)
				}
				r := fetch.RunResults{}
				for _, e := range entries {
					r.Results = append(r.Results, fetch.Result{Source: e.Source, ProjectID: e.ProjectID, Records: e.Records, Error: e.Error, Skipped: e.Skipped})
				}
				c.Results = &r
			}

			accepted, err := check.LoadAcceptances(checksFile())
			if err != nil {
				return err
			}
			report, err := check.Run(cmd.Context(), c, check.Default(), accepted)
			if err != nil {
				return err
			}

			summary := check.Summary(report)
			_, _ = fmt.Fprint(cmd.OutOrStdout(), summary)
			if jsonPath != "" {
				if err := writeReportJSON(jsonPath, report); err != nil {
					return err
				}
			}
			if summaryPath != "" {
				if err := os.WriteFile(summaryPath, []byte(summary), 0o644); err != nil {
					return err
				}
			}
			if report.HasErrors() {
				return fmt.Errorf("check found errors")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&resultsPath, "results", "", "a fetch run's results, from fetch --results")
	cmd.Flags().StringVar(&fetchLog, "fetch-log", "", "check the runs in the fetch log of this day (YYYY-MM-DD)")
	cmd.Flags().StringVar(&jsonPath, "json", "", "write the findings to this file (check.json)")
	cmd.Flags().StringVar(&summaryPath, "summary", "", "write a Markdown summary to this file")
	return cmd
}
