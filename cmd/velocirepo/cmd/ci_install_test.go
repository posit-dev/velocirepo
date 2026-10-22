package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
)

// renderTestWorkflow renders the legacy workflow template with the given
// data and returns the YAML string. Workflows made from it must keep
// working.
func renderTestWorkflow(t *testing.T, data interface{}) string {
	t.Helper()
	tmpl, err := template.New("workflow").Parse(legacyWorkflowTemplate)
	if err != nil {
		t.Fatalf("parse workflow template: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("render workflow template: %v", err)
	}
	return buf.String()
}

func TestLegacyWorkflowTemplateHasConcurrencyGroup(t *testing.T) {
	yaml := renderTestWorkflow(t, struct {
		Cron           string
		Version        string
		NeedsPlausible bool
		NeedsYouTube   bool
	}{Cron: "0 1 * * *", Version: "v0", NeedsPlausible: false, NeedsYouTube: false})

	assertContains(t, yaml, "concurrency:")
	assertContains(t, yaml, "group: velocirepo-fetch")
	assertContains(t, yaml, "cancel-in-progress: false")
}

func TestLegacyWorkflowTemplateCommitStepHasIfNotCancelled(t *testing.T) {
	yaml := renderTestWorkflow(t, struct {
		Cron           string
		Version        string
		NeedsPlausible bool
		NeedsYouTube   bool
	}{Cron: "0 1 * * *", Version: "v0", NeedsPlausible: false, NeedsYouTube: false})

	// The if: condition must be on the Commit and push step.
	commitIdx := strings.Index(yaml, "Commit and push")
	if commitIdx < 0 {
		t.Fatal("Commit and push step not found in template")
	}
	stepYAML := yaml[commitIdx:]
	// Find the next step boundary so we only check the commit step's YAML.
	nextStep := strings.Index(stepYAML[1:], "\n      - ")
	if nextStep > 0 {
		stepYAML = stepYAML[:nextStep+1]
	}
	assertContains(t, stepYAML, "!cancelled()")
}

func TestLegacyWorkflowTemplateCommitStepPullsBeforePush(t *testing.T) {
	yaml := renderTestWorkflow(t, struct {
		Cron           string
		Version        string
		NeedsPlausible bool
		NeedsYouTube   bool
	}{Cron: "0 1 * * *", Version: "v0", NeedsPlausible: false, NeedsYouTube: false})

	pullIdx := strings.Index(yaml, "git pull --rebase")
	pushIdx := strings.Index(yaml, "git push")
	if pullIdx < 0 {
		t.Fatal("git pull --rebase not found in template")
	}
	if pushIdx < 0 {
		t.Fatal("git push not found in template")
	}
	if pullIdx > pushIdx {
		t.Error("git pull --rebase must appear before git push")
	}
}

func TestLegacyWorkflowTemplateOptionalTokens(t *testing.T) {
	withBoth := renderTestWorkflow(t, struct {
		Cron           string
		Version        string
		NeedsPlausible bool
		NeedsYouTube   bool
	}{Cron: "0 1 * * *", Version: "v0", NeedsPlausible: true, NeedsYouTube: true})

	assertContains(t, withBoth, "plausible-token:")
	assertContains(t, withBoth, "youtube-token:")

	withNeither := renderTestWorkflow(t, struct {
		Cron           string
		Version        string
		NeedsPlausible bool
		NeedsYouTube   bool
	}{Cron: "0 1 * * *", Version: "v0", NeedsPlausible: false, NeedsYouTube: false})

	assertNotContains(t, withNeither, "plausible-token:")
	assertNotContains(t, withNeither, "youtube-token:")
}

func TestWorkflowTemplateRendersToFile(t *testing.T) {
	// Light smoke test that install-ci would create a non-empty file.
	dir := t.TempDir()
	outPath := filepath.Join(dir, "velocirepo.yml")

	tmpl, err := template.New("workflow").Parse(workflowTemplate)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := tmpl.Execute(f, struct {
		Cron           string
		Version        string
		NeedsPlausible bool
		NeedsYouTube   bool
	}{"0 1 * * *", "v0", false, false}); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatal("workflow file not created")
	}
	if info.Size() == 0 {
		t.Fatal("workflow file is empty")
	}
}

// "latest" means the action of the binary's own major version. It used to
// be hard-coded as v0, so workflows generated with v1 referenced v0.
func TestActionRef(t *testing.T) {
	for _, tt := range []struct{ ver, binary, want string }{
		{"latest", "v1.0.0", "v1"},
		{"latest", "v2.3.4", "v2"},
		{"latest", "dev", "v1"},
		{"1.2.3", "v1.0.0", "v1.2.3"},
		{"v1.2.3", "v1.0.0", "v1.2.3"},
	} {
		if got := actionRef(tt.ver, tt.binary); got != tt.want {
			t.Errorf("actionRef(%q, %q) = %q, want %q", tt.ver, tt.binary, got, tt.want)
		}
	}
}

// The nightly run is scheduled after every configured source's yesterday has
// ended, plus an hour: for the star history that's US Pacific time, for a
// Plausible site its own zone, or UTC-12 when that's unknown.
func TestSuggestSchedule(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	ny, _ := time.LoadLocation("America/New_York")
	project := func(github, plausible string) config.Project {
		var p config.Project
		if github != "" {
			p.GitHubEvents = config.StringList{github}
		}
		if plausible != "" {
			p.Plausible = config.StringList{plausible}
		}
		return p
	}
	for _, tt := range []struct {
		name     string
		projects map[string]config.Project
		zones    map[string]*time.Location
		want     string
	}{
		{"only UTC sources", map[string]config.Project{"a": {PyPI: config.StringList{"pkg"}}}, nil, "30 1 * * *"},
		{"star history", map[string]config.Project{"a": project("org/a", "")}, nil, "30 9 * * *"},
		{"a site in New York", map[string]config.Project{"a": project("", "ny.example")}, map[string]*time.Location{"ny.example": ny}, "30 6 * * *"},
		{"a site in Los Angeles and stars", map[string]config.Project{"a": project("org/a", "la.example")}, map[string]*time.Location{"la.example": la}, "30 9 * * *"},
		{"a site of unknown zone", map[string]config.Project{"a": project("", "unknown.example")}, nil, "30 13 * * *"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Projects: tt.projects}
			cron, reason := suggestSchedule(cfg, func(site string) *time.Location {
				if z, ok := tt.zones[site]; ok {
					return z
				}
				return time.FixedZone("UTC-12", -12*3600)
			})
			if cron != tt.want || reason == "" {
				t.Errorf("suggestSchedule = %q (%s), want %q", cron, reason, tt.want)
			}
		})
	}
}
