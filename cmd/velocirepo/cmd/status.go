package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/posit-dev/velocirepo/internal/dateutil"
	"github.com/posit-dev/velocirepo/internal/store"
	"github.com/spf13/cobra"
)

// statusRow is one project × source × target line in the fleet status report.
type statusRow struct {
	Project  string `json:"project"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	LastDate string `json:"last_date"`
	AgeDays  *int   `json:"age_days"`
	Stale    bool   `json:"stale"`
}

func statusCmd() *cobra.Command {
	var jsonOutput bool
	var staleDays int
	var staleOnly bool

	cmd := &cobra.Command{
		Use:   "status [project]",
		Short: "Summarize fetch state for each project, source, and target",
		Long: "Report the last date each source/target was successfully fetched, " +
			"using watermarks so unchanged total_* gauges are not misreported as stale.\n\n" +
			"Optionally filter to a single project. Rows older than --stale-days are " +
			"flagged as stale.",
		Args:    cobra.MaximumNArgs(1),
		GroupID: "project",
		RunE: func(cmd *cobra.Command, args []string) error {
			dataDir := cfg.DataDir()

			ids := make([]string, 0, len(cfg.Projects))
			if len(args) == 1 {
				if _, err := cfg.GetProject(args[0]); err != nil {
					return err
				}
				ids = append(ids, args[0])
			} else {
				for id := range cfg.Projects {
					ids = append(ids, id)
				}
			}
			sort.Strings(ids)

			// Anchor age against the configured end date when set, else today.
			now := referenceDate(cfg.Settings.EndDate)

			var rows []statusRow
			for _, id := range ids {
				proj, err := cfg.GetProject(id)
				if err != nil {
					return err
				}
				for _, src := range listSources(proj) {
					rows = append(rows, statusRowsForSource(dataDir, id, src, now, staleDays)...)
				}
			}

			if staleOnly {
				filtered := rows[:0]
				for _, r := range rows {
					if r.Stale {
						filtered = append(filtered, r)
					}
				}
				rows = filtered
			}

			if jsonOutput {
				if rows == nil {
					rows = []statusRow{}
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}

			return renderStatusTable(cmd, rows)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	cmd.Flags().IntVar(&staleDays, "stale-days", 7, "flag targets not fetched within this many days")
	cmd.Flags().BoolVar(&staleOnly, "stale-only", false, "only show stale targets")

	return cmd
}

// statusRowsForSource builds one row per target for a source. Metric sources
// with watermarks report per-target last-checked dates; sources without
// watermarks (or with none recorded yet) fall back to a single source-level row
// derived from store.LastDate.
func statusRowsForSource(dataDir, projectID, src string, now time.Time, staleDays int) []statusRow {
	watermarks, err := store.ReadMetricWatermarks(dataDir, src, projectID)
	if err == nil && len(watermarks) > 0 {
		rows := make([]statusRow, 0, len(watermarks))
		for _, w := range watermarks {
			rows = append(rows, makeStatusRow(projectID, src, w.Target, w.Date, now, staleDays))
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Target < rows[j].Target })
		return rows
	}

	last := ""
	if t, err := store.LastDate(dataDir, src, projectID); err == nil && !t.IsZero() {
		last = dateutil.FormatDate(t)
	}
	return []statusRow{makeStatusRow(projectID, src, "", last, now, staleDays)}
}

func makeStatusRow(projectID, src, target, lastDate string, now time.Time, staleDays int) statusRow {
	row := statusRow{
		Project:  projectID,
		Source:   src,
		Target:   target,
		LastDate: lastDate,
		Stale:    true, // never fetched counts as stale
	}
	if lastDate == "" {
		return row
	}
	if t, err := dateutil.ParseDate(lastDate); err == nil {
		age := int(now.Sub(t).Hours() / 24)
		if age < 0 {
			age = 0
		}
		row.AgeDays = &age
		row.Stale = age > staleDays
	}
	return row
}

func renderStatusTable(cmd *cobra.Command, rows []statusRow) error {
	if len(rows) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "(no data)")
		return nil
	}

	table := tablewriter.NewTable(cmd.OutOrStdout(),
		tablewriter.WithHeaderAutoFormat(tw.Off),
		tablewriter.WithHeaderAlignment(tw.AlignLeft),
		tablewriter.WithRowAlignment(tw.AlignLeft),
		tablewriter.WithRendition(tw.Rendition{
			Symbols: tw.NewSymbols(tw.StyleLight),
		}),
	)
	table.Header([]string{"PROJECT", "SOURCE", "TARGET", "LAST FETCHED", "AGE", "STATUS"})

	for _, r := range rows {
		last := r.LastDate
		age := ""
		status := "ok"
		switch {
		case r.LastDate == "":
			last = "never"
			status = "stale"
		default:
			if r.AgeDays != nil {
				age = fmt.Sprintf("%dd", *r.AgeDays)
			}
			if r.Stale {
				status = "stale"
			}
		}
		_ = table.Append([]string{r.Project, r.Source, r.Target, last, age, status})
	}

	return table.Render()
}

// referenceDate parses the configured end date, falling back to the current day
// when unset or invalid.
func referenceDate(endDate string) time.Time {
	if endDate != "" {
		if t, err := dateutil.ParseDate(endDate); err == nil {
			return t
		}
	}
	return time.Now()
}
