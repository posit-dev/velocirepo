package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

// renderTestWorkflow renders the workflow template with the given data and
// returns the YAML string. It uses the package-level workflowTemplate const
// directly so that any change to the template is automatically covered.
func renderTestWorkflow(t *testing.T, data interface{}) string {
	t.Helper()
	tmpl, err := template.New("workflow").Parse(workflowTemplate)
	if err != nil {
		t.Fatalf("parse workflow template: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("render workflow template: %v", err)
	}
	return buf.String()
}

func TestWorkflowTemplateHasConcurrencyGroup(t *testing.T) {
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

func TestWorkflowTemplateCommitStepHasIfNotCancelled(t *testing.T) {
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

func TestWorkflowTemplateCommitStepPullsBeforePush(t *testing.T) {
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

func TestWorkflowTemplateOptionalTokens(t *testing.T) {
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
