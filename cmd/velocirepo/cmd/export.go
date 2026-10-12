package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/posit-dev/velocirepo/internal/store"
	"github.com/spf13/cobra"
)

func exportCmd() *cobra.Command {
	var format, source, project string

	cmd := &cobra.Command{
		Use:     "export <directory>",
		Short:   "Export data to Parquet or CSV files",
		Long:    "Export metrics, events, and projects to separate files in the given directory.",
		Args:    cobra.ExactArgs(1),
		GroupID: "query",
		RunE: func(cmd *cobra.Command, args []string) error {
			outDir := args[0]
			written, err := store.Export(store.ExportOptions{
				DataDir:    cfg.DataDir(),
				OutDir:     outDir,
				Format:     format,
				Source:     source,
				Project:    project,
				Projects:   projectInfos(),
				Indicators: indicatorDefs(),
			})
			if err != nil {
				return err
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

	cmd.Flags().StringVar(&format, "format", "parquet", "output format: parquet, csv")
	cmd.Flags().StringVar(&source, "source", "", "export only this source")
	cmd.Flags().StringVar(&project, "project", "", "export only this project")

	return cmd
}
