package cmd

import (
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
	"gopkg.in/yaml.v3"
)

var updateGolden = flag.Bool("update", false, "update the golden files")

var goldenData = workflowData{Cron: "30 9 * * *", HeartbeatCron: "30 12 * * *", Version: "v1", Workflow: "velocirepo.yml", NeedsPlausible: true, NeedsYouTube: true}

// The generated workflows match golden files, and pass actionlint when it's
// installed (CI installs it).
func TestWorkflowsMatchGoldenFiles(t *testing.T) {
	dir := t.TempDir()
	workflows := filepath.Join(dir, ".github", "workflows")
	for name, text := range map[string]string{"velocirepo.yml": workflowTemplate, "velocirepo-heartbeat.yml": heartbeatTemplate} {
		path := filepath.Join(workflows, name)
		if err := renderWorkflow(path, text, goldenData); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		golden := filepath.Join("testdata", name)
		if *updateGolden {
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from %s; run go test -run Golden -update", name, golden)
		}
	}
	if _, err := exec.LookPath("actionlint"); err != nil {
		t.Log("actionlint isn't installed")
		return
	}
	out, err := exec.Command("actionlint", filepath.Join(workflows, "velocirepo.yml"), filepath.Join(workflows, "velocirepo-heartbeat.yml")).CombinedOutput()
	if err != nil {
		t.Errorf("actionlint: %v\n%s", err, out)
	}
}

// The nightly workflow grants what the gate needs, and the heartbeat what
// its calls need, and nothing more.
func TestWorkflowPermissions(t *testing.T) {
	for _, c := range []struct {
		text string
		want map[string]string
	}{
		{workflowTemplate, map[string]string{"contents": "write", "issues": "write", "actions": "read"}},
		{heartbeatTemplate, map[string]string{"contents": "read", "issues": "write", "actions": "read"}},
	} {
		path := filepath.Join(t.TempDir(), "w.yml")
		if err := renderWorkflow(path, c.text, goldenData); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(path)
		var w struct {
			Permissions map[string]string `yaml:"permissions"`
		}
		if err := yaml.Unmarshal(data, &w); err != nil {
			t.Fatal(err)
		}
		if len(w.Permissions) != len(c.want) {
			t.Errorf("permissions = %v, want %v", w.Permissions, c.want)
		}
		for k, v := range c.want {
			if w.Permissions[k] != v {
				t.Errorf("permissions = %v, want %v", w.Permissions, c.want)
			}
		}
	}
}

// Old workflows, made from the legacy template, still fetch: the action's
// mode defaults to fetch, which runs velocirepo fetch as before.
func TestOldWorkflowsStillFetch(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Inputs map[string]struct {
			Default string `yaml:"default"`
		} `yaml:"inputs"`
		Runs struct {
			Steps []struct {
				Name string `yaml:"name"`
				If   string `yaml:"if"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatal(err)
	}
	if action.Inputs["mode"].Default != "fetch" {
		t.Errorf("mode defaults to %q", action.Inputs["mode"].Default)
	}
	fetches := false
	for _, s := range action.Runs.Steps {
		if strings.Contains(s.If, "inputs.mode == 'fetch'") && strings.Contains(s.Run, "velocirepo fetch") {
			fetches = true
		}
	}
	if !fetches {
		t.Error("no step runs velocirepo fetch in fetch mode")
	}
	legacy := renderTestWorkflow(t, goldenData)
	assertNotContains(t, legacy, "mode:")
	assertContains(t, legacy, "git push")
}

func TestHeartbeatCron(t *testing.T) {
	for cron, want := range map[string]string{"30 9 * * *": "30 12 * * *", "30 22 * * *": "30 1 * * *", "30 13 * * *": "30 16 * * *"} {
		if got, err := heartbeatCron(cron); err != nil || got != want {
			t.Errorf("heartbeatCron(%q) = %q, %v; want %q", cron, got, err, want)
		}
	}
	if _, err := heartbeatCron("0 */6 * * *"); err == nil {
		t.Error("a schedule that isn't daily was accepted")
	}
	// With a Plausible site in UTC-12, the nightly run is late, and the
	// heartbeat comes after it.
	cfg := &config.Config{Projects: map[string]config.Project{"p": {Plausible: config.StringList{"example.com"}}}}
	cron, _ := suggestSchedule(cfg, func(string) *time.Location { return time.FixedZone("UTC-12", -12*3600) })
	hb, err := heartbeatCron(cron)
	if err != nil || cron != "30 13 * * *" || hb != "30 16 * * *" {
		t.Errorf("schedule %q, heartbeat %q, %v", cron, hb, err)
	}
}

func TestOwnerHasAccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/data/collaborators/alice", "/orgs/org/teams/devrel/repos/org/data":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	for owner, want := range map[string]bool{"alice": true, "@alice": true, "bob": false, "@org/devrel": true, "@org/other": false} {
		got, err := ownerHasAccess(t.Context(), srv.Client(), srv.URL, "t", "org/data", owner)
		if err != nil || got != want {
			t.Errorf("%s: %v, %v; want %v", owner, got, err, want)
		}
	}
}
