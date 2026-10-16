package cmd

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/posit-dev/velocirepo/internal/dateutil"
	"github.com/posit-dev/velocirepo/internal/store"
	"github.com/spf13/cobra"
)

func queryCmd() *cobra.Command {
	var jsonFlag, csvFlag, parquetFlag, noHeader bool

	cmd := &cobra.Command{
		Use:     "query <sql>",
		Short:   "Run a SQL query against the metrics data",
		Args:    cobra.ExactArgs(1),
		GroupID: "query",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if parquetFlag {
				return writeParquet(out, args[0])
			}

			results, cols, err := store.QueryLive(cfg.DataDir(), projectInfos(), indicatorDefs(), args[0])
			if err != nil {
				return err
			}

			switch {
			case jsonFlag:
				return printJSON(out, results)
			case csvFlag:
				return printCSV(out, results, cols, noHeader)
			case noHeader:
				return printPlain(out, results, cols)
			default:
				return printTable(out, results, cols)
			}
		},
	}

	cmd.Flags().BoolVar(&jsonFlag, "json", false, "output as JSON")
	cmd.Flags().BoolVar(&csvFlag, "csv", false, "output as CSV")
	cmd.Flags().BoolVar(&parquetFlag, "parquet", false, "output as Parquet")
	cmd.Flags().BoolVar(&noHeader, "no-header", false, "omit column headers (plain tab-separated output)")

	return cmd
}

func schemaCmd() *cobra.Command {
	var jsonFlag, csvFlag bool

	cmd := &cobra.Command{
		Use:     "schema",
		Short:   "Show table schemas",
		GroupID: "query",
		RunE: func(cmd *cobra.Command, args []string) error {
			cols, err := store.SchemaLive(cfg.DataDir(), projectInfos(), indicatorDefs())
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			switch {
			case jsonFlag:
				results := make([]map[string]interface{}, len(cols))
				for i, c := range cols {
					results[i] = map[string]interface{}{
						"table":  c.Table,
						"column": c.Column,
						"type":   c.Type,
					}
				}
				return printJSON(out, results)
			case csvFlag:
				w := csv.NewWriter(out)
				_ = w.Write([]string{"table", "column", "type"})
				for _, c := range cols {
					_ = w.Write([]string{c.Table, c.Column, c.Type})
				}
				w.Flush()
				return w.Error()
			default:
				table := tablewriter.NewTable(out,
					tablewriter.WithHeaderAutoFormat(tw.Off),
					tablewriter.WithHeaderAlignment(tw.AlignLeft),
					tablewriter.WithRowAlignment(tw.AlignLeft),
					tablewriter.WithRendition(tw.Rendition{
						Symbols: tw.NewSymbols(tw.StyleLight),
					}),
				)
				table.Header([]string{"TABLE", "COLUMN", "TYPE"})
				for _, c := range cols {
					_ = table.Append([]string{c.Table, c.Column, c.Type})
				}
				_ = table.Render()
				return nil
			}
		},
	}

	cmd.Flags().BoolVar(&jsonFlag, "json", false, "output as JSON")
	cmd.Flags().BoolVar(&csvFlag, "csv", false, "output as CSV")

	return cmd
}

func writeParquet(out io.Writer, query string) error {
	tmp, err := os.CreateTemp("", "velocirepo-*.parquet")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := store.QueryLiveParquet(cfg.DataDir(), projectInfos(), indicatorDefs(), query, tmpPath); err != nil {
		return err
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	_, err = io.Copy(out, f)
	return err
}

func printTable(out io.Writer, results []map[string]interface{}, cols []string) error {
	if len(results) == 0 {
		_, _ = fmt.Fprintln(out, "(0 rows)")
		return nil
	}

	table := tablewriter.NewTable(out,
		tablewriter.WithHeaderAutoFormat(tw.Off),
		tablewriter.WithRendition(tw.Rendition{
			Symbols: tw.NewSymbols(tw.StyleLight),
		}),
	)
	table.Header(cols)

	for _, row := range results {
		vals := make([]string, len(cols))
		for i, col := range cols {
			vals[i] = formatValue(row[col])
		}
		_ = table.Append(vals)
	}

	_ = table.Render()
	return nil
}

func printJSON(out io.Writer, results []map[string]interface{}) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}

func printCSV(out io.Writer, results []map[string]interface{}, cols []string, noHeader bool) error {
	if len(results) == 0 {
		return nil
	}

	w := csv.NewWriter(out)
	if !noHeader {
		_ = w.Write(cols)
	}
	for _, row := range results {
		vals := make([]string, len(cols))
		for i, col := range cols {
			vals[i] = formatValue(row[col])
		}
		_ = w.Write(vals)
	}
	w.Flush()
	return w.Error()
}

func printPlain(out io.Writer, results []map[string]interface{}, cols []string) error {
	for _, row := range results {
		vals := make([]string, len(cols))
		for i, col := range cols {
			vals[i] = formatValue(row[col])
		}
		if len(vals) == 1 {
			_, _ = fmt.Fprintln(out, vals[0])
		} else {
			_, _ = fmt.Fprintln(out, strings.Join(vals, "\t"))
		}
	}
	return nil
}

func formatValue(v interface{}) string {
	switch val := v.(type) {
	case time.Time:
		if val.Hour() == 0 && val.Minute() == 0 && val.Second() == 0 && val.Nanosecond() == 0 {
			return dateutil.FormatDate(val)
		}
		return val.Format("2006-01-02T15:04:05Z")
	case nil:
		return "<null>"
	default:
		return fmt.Sprintf("%v", v)
	}
}

func projectInfos() []store.ProjectInfo {
	return cfg.ProjectInfos()
}

func indicatorDefs() []store.IndicatorDef {
	return cfg.IndicatorDefs()
}
