package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/posit-dev/velocirepo/internal/datadict"
	"github.com/posit-dev/velocirepo/internal/store"
	"github.com/posit-dev/velocirepo/internal/ui"
	"github.com/spf13/cobra"
)

func exportCmd() *cobra.Command {
	var format, source, project string
	var dataDict bool

	cmd := &cobra.Command{
		Use:   "export <directory>",
		Short: "Export data to Parquet, CSV or a standalone DuckDB database",
		Long: `Export the data as plain tables to the given directory: one file per table for parquet
and csv, or one database file (velocirepo.duckdb) for duckdb. The tables are projects,
targets, metrics, metrics_filled, indicators, events, content and meta.

With --data-dict, also write data-dict.yaml, a data dictionary (https://data-dict.tidyverse.org)
of exactly the exported data, with what [data_dict] in velocirepo.toml adds. The dictionary
describes the duckdb format, which --data-dict implies.`,
		Args:    cobra.ExactArgs(1),
		GroupID: "query",
		RunE: func(cmd *cobra.Command, args []string) error {
			outDir := args[0]
			if dataDict {
				if !cmd.Flags().Changed("format") {
					format = "duckdb"
				} else if format != "duckdb" {
					return fmt.Errorf("--data-dict describes the duckdb format; leave out --format or use --format duckdb")
				}
			}
			opts := store.ExportOptions{
				DataDir:    cfg.DataDir(),
				OutDir:     outDir,
				Format:     format,
				Source:     source,
				Project:    project,
				Projects:   projectInfos(),
				Indicators: indicatorDefs(),
			}
			x, err := store.Materialize(opts)
			if err != nil {
				return err
			}
			defer func() { _ = x.Close() }()

			written, err := x.Write(outDir, format)
			if err != nil {
				return err
			}
			if dataDict {
				dict, warnings, err := datadict.Generate(x, datadict.Options{
					Config:     cfg,
					Indicators: indicatorDefs(),
					Source:     source,
					Project:    project,
				})
				if err != nil {
					return err
				}
				data, err := dict.YAML()
				if err != nil {
					return err
				}
				path := filepath.Join(outDir, datadict.FileName)
				if err := os.WriteFile(path, data, 0644); err != nil {
					return fmt.Errorf("write %s: %w", path, err)
				}
				written = append(written, path)
				for _, w := range warnings {
					ui.Warnf("data dictionary: %s", w)
				}
			}

			for _, path := range written {
				info, err := os.Stat(path)
				if err != nil {
					continue
				}
				name := filepath.Join(outDir, filepath.Base(path))
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s (%s)\n", name, formatSize(info.Size()))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&format, "format", "parquet", "output format: parquet, csv, duckdb")
	cmd.Flags().StringVar(&source, "source", "", "export only this source")
	cmd.Flags().StringVar(&project, "project", "", "export only this project")
	cmd.Flags().BoolVar(&dataDict, "data-dict", false, "also write data-dict.yaml, a data dictionary of the export (implies --format duckdb)")

	return cmd
}
