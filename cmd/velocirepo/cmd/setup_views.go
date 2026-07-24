package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/posit-dev/velocirepo/internal/views"
	"github.com/spf13/cobra"
)

func setupViewsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "setup-views",
		Short:   "Install dependencies for all views",
		Long:    "Run uv sync for Python views. R views bootstrap their environment automatically on first render via ir, so they need no setup step.",
		GroupID: "view",
		RunE: func(cmd *cobra.Command, args []string) error {
			viewsDir := cfg.ViewsDir()
			allViews, err := views.Discover(viewsDir)
			if err != nil {
				return err
			}

			if len(allViews) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No views found.")
				return nil
			}

			out := cmd.OutOrStdout()
			var setupCount int
			for _, v := range allViews {
				didSetup := false

				if _, err := os.Stat(filepath.Join(v.Dir, "pyproject.toml")); err == nil {
					_, _ = fmt.Fprintf(out, "Setting up '%s' (uv sync)...\n", v.Name)
					c := exec.Command("uv", "sync")
					c.Dir = v.Dir
					c.Stdout = os.Stdout
					c.Stderr = os.Stderr
					if err := c.Run(); err != nil {
						return fmt.Errorf("uv sync in %s: %w", v.Name, err)
					}
					didSetup = true
				}

				if didSetup {
					setupCount++
				}
			}

			_, _ = fmt.Fprintf(out, "Set up %d view(s)\n", setupCount)
			return nil
		},
	}
}
