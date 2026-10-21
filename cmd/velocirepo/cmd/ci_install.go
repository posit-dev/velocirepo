package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/posit-dev/velocirepo/internal/version"
	"github.com/spf13/cobra"
)

const workflowTemplate = `name: Fetch Metrics

on:
  schedule:
    - cron: '{{.Cron}}'
  workflow_dispatch:

# Prevent concurrent fetches from racing over the same data files. A run that
# is already in progress is allowed to finish (cancel-in-progress: false).
concurrency:
  group: velocirepo-fetch
  cancel-in-progress: false

permissions:
  contents: write

jobs:
  fetch:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: posit-dev/velocirepo@{{.Version}}
        with:
          github-token: ${{"{{"}} secrets.GH_TOKEN {{"}}"}}
{{- if .NeedsPlausible}}
          plausible-token: ${{"{{"}} secrets.PLAUSIBLE_TOKEN {{"}}"}}
{{- end}}
{{- if .NeedsYouTube}}
          youtube-token: ${{"{{"}} secrets.YOUTUBE_TOKEN {{"}}"}}
{{- end}}

      - name: Commit and push
        # Run even when fetch exits non-zero (some jobs failed) so that data
        # from successful jobs is persisted while the workflow still shows red.
        if: ${{"{{"}} !cancelled() {{"}}"}}
        run: |
          git config --local user.email "github-actions[bot]@users.noreply.github.com"
          git config --local user.name "github-actions[bot]"
          git add velocirepo/
          git diff --staged --quiet || git commit -m "Update metrics - $(date -u +'%Y-%m-%d')"
          git pull --rebase
          git push
`

func installCICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "install-ci",
		Short:   "Generate GitHub Actions workflow for nightly metric fetching",
		GroupID: "ci",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isInteractive() {
				return fmt.Errorf("ci install requires an interactive terminal")
			}

			out := cmd.OutOrStdout()
			reader := bufio.NewReader(os.Stdin)

			defaultCron := "0 1 * * *"
			cron, err := prompt(out, reader, "Cron schedule", defaultCron, "daily at 1am UTC")
			if err != nil {
				return err
			}

			defaultVersion := "latest"
			if version.Version != "dev" {
				defaultVersion = version.Version
			}
			versionHint := "use a tag like v0.1.4, or \"latest\""
			ver, err := prompt(out, reader, "Velocirepo version", defaultVersion, versionHint)
			if err != nil {
				return err
			}

			defaultFile := ".github/workflows/velocirepo.yml"
			filename, err := prompt(out, reader, "Workflow file", defaultFile, "")
			if err != nil {
				return err
			}

			outPath := filepath.Join(cfg.Dir, filename)

			if _, err := os.Stat(outPath); err == nil {
				ok, err := confirm(out, reader, fmt.Sprintf("File %s already exists. Overwrite?", filename))
				if err != nil {
					return err
				}
				if !ok {
					_, _ = fmt.Fprintln(out, "Cancelled.")
					return nil
				}
			}

			needsPlausible := false
			needsYouTube := false
			projects := cfg.Projects
			for _, p := range projects {
				if !p.Plausible.IsEmpty() {
					needsPlausible = true
				}
				if !p.YouTube.IsEmpty() {
					needsYouTube = true
				}
			}

			tmpl, err := template.New("workflow").Parse(workflowTemplate)
			if err != nil {
				return fmt.Errorf("parse template: %w", err)
			}

			outDir := filepath.Dir(outPath)
			if err := os.MkdirAll(outDir, 0755); err != nil {
				return fmt.Errorf("create directory: %w", err)
			}

			f, err := os.Create(outPath)
			if err != nil {
				return fmt.Errorf("create workflow file: %w", err)
			}
			defer func() { _ = f.Close() }()

			actionRef := actionRef(ver, version.Version)

			err = tmpl.Execute(f, struct {
				Cron           string
				Version        string
				NeedsPlausible bool
				NeedsYouTube   bool
			}{
				Cron:           cron,
				Version:        actionRef,
				NeedsPlausible: needsPlausible,
				NeedsYouTube:   needsYouTube,
			})
			if err != nil {
				return fmt.Errorf("render template: %w", err)
			}

			_, _ = fmt.Fprintf(out, "Created %s\n", outPath)
			return nil
		},
	}

	return cmd
}

// defaultActionMajor is the action's major version for development builds,
// which have no version of their own.
const defaultActionMajor = "v1"

// actionRef returns the ref of the velocirepo action for a workflow. "latest"
// means the floating tag of the binary's own major version, such as v1.
func actionRef(ver, binaryVersion string) string {
	if ver != "latest" {
		if !strings.HasPrefix(ver, "v") {
			return "v" + ver
		}
		return ver
	}
	major, _, _ := strings.Cut(strings.TrimPrefix(binaryVersion, "v"), ".")
	if major == "" || strings.Trim(major, "0123456789") != "" {
		return defaultActionMajor
	}
	return "v" + major
}
