package cmd

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/version"
	"github.com/spf13/cobra"
)

// workflowTemplate is the nightly workflow: the gate, which fetches, checks,
// commits and pushes, and reports in the tracking issue.
const workflowTemplate = `name: Fetch Metrics

on:
  schedule:
    - cron: '{{.Cron}}'
  workflow_dispatch:
    inputs:
      release:
        description: "Land a project's held-back data: <source>/<project>"
        required: false
        default: ""
      discard:
        description: "Discard a project's held-back data: <source>/<project>"
        required: false
        default: ""
      repair:
        description: "Re-fetch or replay a range: <source>/<project> <from>..<to>"
        required: false
        default: ""

# One run at a time: a run that is in progress is allowed to finish.
concurrency:
  group: velocirepo-gate
  cancel-in-progress: false

permissions:
  contents: write  # commit the data, and store the archive as release assets
  issues: write    # the tracking issue
  actions: read    # archives saved as artifacts by earlier runs

jobs:
  gate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          # The history back to the last commit the gate checked.
          fetch-depth: 0

      - uses: posit-dev/velocirepo@{{.Version}}
        with:
          mode: gate
          github-token: ${{"{{"}} secrets.GH_TOKEN {{"}}"}}
{{- if .NeedsPlausible}}
          plausible-token: ${{"{{"}} secrets.PLAUSIBLE_TOKEN {{"}}"}}
{{- end}}
{{- if .NeedsYouTube}}
          youtube-token: ${{"{{"}} secrets.YOUTUBE_TOKEN {{"}}"}}
{{- end}}
          release: ${{"{{"}} inputs.release {{"}}"}}
          discard: ${{"{{"}} inputs.discard {{"}}"}}
          repair: ${{"{{"}} inputs.repair {{"}}"}}
          # Optional: a Healthchecks ping URL, for a heartbeat outside GitHub.
          healthchecks-url: ${{"{{"}} secrets.HEALTHCHECKS_URL {{"}}"}}
`

// heartbeatTemplate checks every day that the nightly workflow ran, since a
// workflow that doesn't run can't report that itself.
const heartbeatTemplate = `name: Check the nightly run

on:
  schedule:
    - cron: '{{.HeartbeatCron}}'
  workflow_dispatch:

permissions:
  contents: read  # the nightly workflow's schedule
  issues: write   # the tracking issue
  actions: read   # the nightly workflow's runs

jobs:
  heartbeat:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          sparse-checkout: .github/workflows

      - uses: posit-dev/velocirepo@{{.Version}}
        with:
          mode: check-heartbeat
          fetch-args: --workflow {{.Workflow}}
`

// digestTemplate posts the weekly digest, for the problems no check was
// written for.
const digestTemplate = `name: Weekly digest

on:
  schedule:
    - cron: '{{.DigestCron}}'
  workflow_dispatch:

permissions:
  contents: read  # the data
  issues: write   # the digest issue

jobs:
  digest:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: posit-dev/velocirepo@{{.Version}}
        with:
          mode: digest
`

// legacyWorkflowTemplate is the workflow install-ci generated before the
// gate. Workflows made from it keep working: the action's mode defaults to
// fetch.
const legacyWorkflowTemplate = `name: Fetch Metrics

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

// workflowData fills in the templates.
type workflowData struct {
	Cron           string
	HeartbeatCron  string
	DigestCron     string
	Version        string
	Workflow       string
	NeedsPlausible bool
	NeedsYouTube   bool
}

// heartbeatDelay is how many hours after the nightly run's scheduled time
// the heartbeat checks that it started.
const heartbeatDelay = 3

// heartbeatCron returns the heartbeat's schedule: three hours after the
// nightly run's, which must be daily at a fixed time.
func heartbeatCron(cron string) (string, error) {
	fields := strings.Fields(cron)
	bad := fmt.Errorf(`the heartbeat needs a daily schedule at a fixed time, such as "30 9 * * *"; got %q`, cron)
	if len(fields) != 5 || fields[2] != "*" || fields[3] != "*" || fields[4] != "*" {
		return "", bad
	}
	minute, err1 := strconv.Atoi(fields[0])
	hour, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil {
		return "", bad
	}
	return fmt.Sprintf("%d %d * * *", minute, (hour+heartbeatDelay)%24), nil
}

// digestCron returns the digest's schedule: Mondays, an hour after the
// heartbeat.
func digestCron(heartbeat string) string {
	fields := strings.Fields(heartbeat)
	hour, _ := strconv.Atoi(fields[1])
	return fmt.Sprintf("%s %d * * 1", fields[0], (hour+1)%24)
}

// renderWorkflow writes a template to path.
func renderWorkflow(path, text string, data workflowData) error {
	tmpl, err := template.New("workflow").Parse(text)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create workflow file: %w", err)
	}
	if err := tmpl.Execute(f, data); err != nil {
		_ = f.Close()
		return fmt.Errorf("render template: %w", err)
	}
	return f.Close()
}

func installCICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "install-ci",
		Short:   "Generate GitHub Actions workflows for the nightly run and its heartbeat",
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
			hbCron, err := heartbeatCron(cron)
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

			data := workflowData{Cron: cron, HeartbeatCron: hbCron, DigestCron: digestCron(hbCron), Version: actionRef(ver, version.Version), Workflow: filepath.Base(filename)}
			for _, p := range cfg.Projects {
				data.NeedsPlausible = data.NeedsPlausible || !p.Plausible.IsEmpty()
				data.NeedsYouTube = data.NeedsYouTube || !p.YouTube.IsEmpty()
			}

			files := []struct{ path, text string }{
				{filepath.Join(cfg.Dir, filename), workflowTemplate},
				{filepath.Join(cfg.Dir, filepath.Dir(filename), "velocirepo-heartbeat.yml"), heartbeatTemplate},
				{filepath.Join(cfg.Dir, filepath.Dir(filename), "velocirepo-digest.yml"), digestTemplate},
			}
			for _, f := range files {
				if _, err := os.Stat(f.path); err == nil {
					ok, err := confirm(out, reader, fmt.Sprintf("File %s already exists. Overwrite?", f.path))
					if err != nil {
						return err
					}
					if !ok {
						_, _ = fmt.Fprintln(out, "Cancelled.")
						return nil
					}
				}
			}
			for _, f := range files {
				if err := renderWorkflow(f.path, f.text, data); err != nil {
					return err
				}
				_, _ = fmt.Fprintf(out, "Created %s\n", f.path)
			}

			if err := promptOwners(cmd.Context(), out, reader); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(out, "No repository settings need to change: the workflow's own token commits, keeps the tracking issue and stores the archive.")
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
