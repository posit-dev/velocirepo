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
	var resultsPath, fetchLog, jsonPath, summaryPath, base, head string
	var repairs []string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check stored data and a fetch run for problems",
		Long: `Run velocirepo's checks and report their findings. Exits non-zero when there are errors that aren't accepted in velocirepo/checks.toml.

In a Git repository, the data is also compared with HEAD: a fetch only adds data, so removed records, changed values and removed content entries are reported. With --base and --head, two commits are compared instead, for example to check commits made by hand. Changes within a repair, given with --repair or a Velocirepo-Repair trailer, are warnings.`,
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

			if head != "" && base == "" {
				return fmt.Errorf("--head needs --base")
			}
			d := &check.Diff{Base: base, Head: head}
			for _, r := range repairs {
				repair, err := check.ParseRepair(r)
				if err != nil {
					return err
				}
				d.Repairs = append(d.Repairs, repair)
			}
			if d.Base == "" && check.HasCommit(cmd.Context(), cfg.DataDir(), "HEAD") {
				d.Base = "HEAD"
			}
			if d.Base != "" {
				c.Diff = d
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
	cmd.Flags().StringVar(&base, "base", "", "compare the data with this commit (default: HEAD)")
	cmd.Flags().StringVar(&head, "head", "", "compare this commit with --base, instead of the work tree")
	cmd.Flags().StringArrayVar(&repairs, "repair", nil, "changes within this repair are deliberate: <source>/<project> <from>..<to>")
	return cmd
}
