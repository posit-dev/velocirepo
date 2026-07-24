package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/posit-dev/velocirepo/internal/views"
	"github.com/spf13/cobra"
)

func addViewCmd() *cobra.Command {
	var framework, source string
	var noUV bool

	cmd := &cobra.Command{
		Use:     "add-view <name>",
		Short:   "Scaffold a new view directory",
		Long:    "Create a new view directory with render.sh and template files. The name can include slashes for subdirectories (e.g., reports/weekly-stars).",
		Args:    cobra.ExactArgs(1),
		GroupID: "view",
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			fw, err := views.ParseFramework(framework)
			if err != nil {
				return err
			}

			if source != "duckdb" && source != "parquet" {
				return fmt.Errorf("invalid source %q (use duckdb or parquet)", source)
			}

			viewsDir := cfg.ViewsDir()
			viewDir, err := views.ScaffoldDir(viewsDir, name)
			if err != nil {
				return err
			}

			var dbPath, dataDir string
			if source == "duckdb" {
				absViewDir, _ := filepath.Abs(viewDir)
				absDataDir, _ := filepath.Abs(cfg.DataDir())
				dbFile := filepath.Join(absDataDir, "velocirepo.duckdb")
				rel, err := filepath.Rel(absViewDir, dbFile)
				if err != nil {
					rel = dbFile
				}
				dbPath = filepath.ToSlash(rel)
			} else {
				absViewDir, _ := filepath.Abs(viewDir)
				absDataDir, _ := filepath.Abs(filepath.Join(viewsDir, "_data"))
				rel, err := filepath.Rel(absViewDir, absDataDir)
				if err != nil {
					rel = absDataDir
				}
				dataDir = filepath.ToSlash(rel)
			}

			dir, err := views.Scaffold(views.ScaffoldOptions{
				ViewsDir:  viewsDir,
				Name:      name,
				Framework: fw,
				Source:    source,
				DBPath:    dbPath,
				DataDir:   dataDir,
				NoUV:      noUV,
			})
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Created view '%s' at %s\n", name, dir)
			return nil
		},
	}

	cmd.Flags().StringVarP(&framework, "framework", "f", "", "framework: quarto-python, quarto-r, jupyter, marimo, r, sql (required)")
	cmd.Flags().StringVarP(&source, "source", "s", "duckdb", "data source: duckdb or parquet")
	cmd.Flags().BoolVar(&noUV, "no-uv", false, "skip pyproject.toml generation")
	_ = cmd.MarkFlagRequired("framework")

	return cmd
}
