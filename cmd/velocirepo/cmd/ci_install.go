package cmd

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/source"
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

			defaultCron, reason := suggestSchedule(cfg, plausibleZoneLookup(cmd.Context(), cfg))
			cron, err := prompt(out, reader, "Cron schedule", defaultCron, reason)
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

// suggestSchedule returns a cron schedule for the nightly run: an hour after
// yesterday has ended in the latest time zone among the configured sources,
// at half past. Days are fetched once they've ended in their source's time
// zone, so at that time every source is fetched through the same day. zoneOf
// returns a Plausible site's time zone.
func suggestSchedule(cfg *config.Config, zoneOf func(site string) *time.Location) (string, string) {
	latestOffset := 0
	latestName := "UTC"
	consider := func(loc *time.Location, name string) {
		// The winter and summer offsets differ; the latest wins.
		for _, month := range []time.Month{time.January, time.July} {
			_, offset := time.Date(2026, month, 15, 12, 0, 0, 0, loc).Zone()
			if offset < latestOffset {
				latestOffset, latestName = offset, name
			}
		}
	}

	stars, _ := time.LoadLocation("America/Los_Angeles")
	for _, p := range cfg.Projects {
		if !p.GitHubEvents.IsEmpty() {
			consider(stars, "US Pacific time, for the star history")
		}
		for _, site := range p.Plausible {
			if zone, ok := cfg.Settings.Timezones[site]; ok {
				if loc, err := time.LoadLocation(zone); err == nil {
					consider(loc, zone+", for "+site)
					continue
				}
			}
			loc := zoneOf(site)
			consider(loc, loc.String()+", for "+site)
		}
	}

	hour := -latestOffset/3600 + 1
	reason := fmt.Sprintf("daily at %02d:30 UTC, an hour after yesterday has ended in %s", hour, latestName)
	return fmt.Sprintf("30 %d * * *", hour), reason
}

// plausibleZoneLookup returns a function that looks up a Plausible site's
// time zone, using PLAUSIBLE_TOKEN when it's set. Without it, sites are
// treated as being in UTC-12, which schedules the run later than needed;
// setting [settings.timezones] avoids that.
func plausibleZoneLookup(ctx context.Context, cfg *config.Config) func(string) *time.Location {
	token := os.Getenv("PLAUSIBLE_TOKEN")
	client := &http.Client{Timeout: 10 * time.Second}
	return func(site string) *time.Location {
		p := &source.Plausible{Client: client, APIKey: token, SiteID: site, TimeZone: cfg.Settings.Timezones[site]}
		if token == "" && p.TimeZone == "" {
			return time.FixedZone("UTC-12", -12*3600)
		}
		loc, err := p.DayZone(ctx, "")
		if err != nil {
			return time.FixedZone("UTC-12", -12*3600)
		}
		return loc
	}
}
