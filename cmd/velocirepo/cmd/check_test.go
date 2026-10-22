package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/posit-dev/velocirepo/internal/store"
)

// check reads a past run from the fetch log, fails on its errors, and passes
// once they're accepted in velocirepo/checks.toml.
func TestCheckCommand(t *testing.T) {
	cfgPath := setupTestConfig(t, `[data]
dir = "velocirepo/data"

[projects.positron]
github = "posit-dev/positron"
`)
	dataDir := filepath.Join(filepath.Dir(cfgPath), "velocirepo", "data")
	if err := store.WriteLog(dataDir, []store.LogEntry{{RunID: "2026-10-20T06:00:00Z", Timestamp: "2026-10-20T06:00:00Z", Source: "github", ProjectID: "positron", Error: "comments: 422"}}); err != nil {
		t.Fatal(err)
	}
	logs, _ := filepath.Glob(filepath.Join(dataDir, "log", "*.jsonl"))
	date := strings.TrimSuffix(filepath.Base(logs[0]), ".jsonl")

	jsonPath := filepath.Join(t.TempDir(), "check.json")
	_, out, err := execCmd(cfgPath, "check", "--fetch-log", date, "--json", jsonPath)
	if err == nil || !strings.Contains(out.String(), "fetch.failed") {
		t.Fatalf("err = %v, output = %s", err, out.String())
	}
	report := readFileString(t, jsonPath)
	start := strings.Index(report, `"id": "`) + len(`"id": "`)
	id := report[start : start+12]

	checks := filepath.Join(filepath.Dir(cfgPath), "velocirepo", "checks.toml")
	if err := os.WriteFile(checks, []byte("[[accept]]\nid = \""+id+"\"\nreason = \"fixed in v1.0.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, out, err := execCmd(cfgPath, "check", "--fetch-log", date); err != nil {
		t.Errorf("an accepted finding still fails: %v\n%s", err, out.String())
	}
}
