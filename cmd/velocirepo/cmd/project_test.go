package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/posit-dev/velocirepo/internal/store"
)

// currentSchemaVersion is the on-disk .schema-version contents for the latest
// schema, so tests that seed data directories pass CheckSchemaVersion.
var currentSchemaVersion = fmt.Sprintf("%d\n", store.LatestSchemaVersion)

func TestProjectListTable(t *testing.T) {
	cfgPath := setupTestConfig(t, `[projects.alpha]
name = "Alpha"
github ="org/alpha"

[projects.beta]
name = "Beta"
pypi = "beta-pkg"
`)

	_, buf, err := execCmd(cfgPath, "list-projects")
	if err != nil {
		t.Fatal(err)
	}

	output := buf.String()
	assertContains(t, output, "alpha")
	assertContains(t, output, "beta")
}

func TestProjectListJSON(t *testing.T) {
	cfgPath := setupTestConfig(t, `[projects.myproj]
name = "My Project"
github ="me/myproj"
`)

	_, buf, err := execCmd(cfgPath, "list-projects", "--json")
	if err != nil {
		t.Fatal(err)
	}

	var list []map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &list); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, buf.String())
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 project, got %d", len(list))
	}
	if list[0]["id"] != "myproj" {
		t.Errorf("id = %v, want 'myproj'", list[0]["id"])
	}
}

func TestProjectListQuiet(t *testing.T) {
	cfgPath := setupTestConfig(t, `[projects.alpha]
name = "Alpha"
github ="org/alpha"

[projects.beta]
name = "Beta"
github ="org/beta"
`)

	_, buf, err := execCmd(cfgPath, "list-projects", "--ids-only")
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
}

func TestProjectListEmpty(t *testing.T) {
	cfgPath := setupTestConfig(t, `[data]
dir = "data"
`)

	_, buf, err := execCmd(cfgPath, "list-projects")
	if err != nil {
		t.Fatal(err)
	}

	assertContains(t, buf.String(), "No projects")
}

func TestProjectAddWithFlags(t *testing.T) {
	cfgPath := setupTestConfig(t, `[data]
dir = "data"
`)

	_, _, err := execCmd(cfgPath, "add-project", "myproj", "--github", "org/myproj", "--pypi", "myproj")
	if err != nil {
		t.Fatal(err)
	}

	content := readFileString(t, cfgPath)
	assertContains(t, content, "[projects.myproj]")
	assertContains(t, content, `github = "org/myproj"`)
	assertContains(t, content, `pypi = "myproj"`)
}

func TestProjectAddDuplicate(t *testing.T) {
	cfgPath := setupTestConfig(t, `[projects.existing]
name = "Existing"
github ="org/existing"
`)

	_, _, err := execCmd(cfgPath, "add-project", "existing", "--github", "org/other")
	if err == nil {
		t.Fatal("expected error for duplicate project")
	}
}

func TestProjectAddInvalidID(t *testing.T) {
	cfgPath := setupTestConfig(t, `[data]
dir = "data"
`)

	_, _, err := execCmd(cfgPath, "add-project", "INVALID_ID", "--github", "org/repo")
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
}

func TestProjectAddInvalidGitHub(t *testing.T) {
	cfgPath := setupTestConfig(t, `[data]
dir = "data"
`)

	_, _, err := execCmd(cfgPath, "add-project", "myproj", "--github", "not-a-valid-format")
	if err == nil {
		t.Fatal("expected error for invalid GitHub format")
	}
}

func TestProjectRemoveForce(t *testing.T) {
	cfgPath := setupTestConfig(t, `[projects.alpha]
name = "Alpha"
github ="org/alpha"

[projects.beta]
name = "Beta"
github ="org/beta"
`)

	_, _, err := execCmd(cfgPath, "remove-project", "alpha", "--force")
	if err != nil {
		t.Fatal(err)
	}

	content := readFileString(t, cfgPath)
	assertNotContains(t, content, "alpha")
	assertContains(t, content, "[projects.beta]")
}

func TestProjectRemoveNotFound(t *testing.T) {
	cfgPath := setupTestConfig(t, `[projects.alpha]
name = "Alpha"
github ="org/alpha"
`)

	_, _, err := execCmd(cfgPath, "remove-project", "nonexistent", "--force")
	if err == nil {
		t.Fatal("expected error for nonexistent project")
	}
}

func TestProjectRemoveWithDeleteData(t *testing.T) {
	cfgPath := setupTestConfig(t, `[data]
dir = "data"

[projects.alpha]
name = "Alpha"
github ="org/alpha"
`)
	dir := filepath.Dir(cfgPath)

	dataDir := filepath.Join(dir, "data", "events", "github", "alpha")
	_ = os.MkdirAll(dataDir, 0755)
	writeTempFile(t, dataDir, "2025-01-01.jsonl", `{}`)
	watermarkDir := filepath.Join(dir, "data", "metrics", "pypi", "alpha")
	_ = os.MkdirAll(watermarkDir, 0755)
	writeTempFile(t, watermarkDir, "_watermark.json", `{"source":"pypi","project_id":"alpha","target":"alpha","date":"2025-01-01"}`)
	writeTempFile(t, filepath.Join(dir, "data"), ".schema-version", currentSchemaVersion)

	_, _, err := execCmd(cfgPath, "remove-project", "alpha", "--force", "--delete-data")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Error("data directory should have been deleted")
	}
	if _, err := os.Stat(watermarkDir); !os.IsNotExist(err) {
		t.Error("watermark directory should have been deleted")
	}
}

func TestProjectRemoveWithDeleteDataRollsBackWhenConfigWriteFails(t *testing.T) {
	cfgPath := setupTestConfig(t, `[data]
dir = "data"

[projects.alpha]
name = "Alpha"
github ="org/alpha"
`)
	dir := filepath.Dir(cfgPath)

	dataDir := filepath.Join(dir, "data", "events", "github", "alpha")
	_ = os.MkdirAll(dataDir, 0755)
	dataFile := writeTempFile(t, dataDir, "2025-01-01.jsonl", `{}`)

	removeErr := errors.New("write denied")
	err := removeProjectConfigAndData(cfgPath, filepath.Join(dir, "data"), "alpha", true, func(path, id string) error {
		return removeErr
	})
	if !errors.Is(err, removeErr) {
		t.Fatalf("expected wrapped config error, got %v", err)
	}
	if _, err := os.Stat(dataFile); err != nil {
		t.Fatalf("data file should have been restored after config failure: %v", err)
	}
	content := readFileString(t, cfgPath)
	assertContains(t, content, "[projects.alpha]")
	matches, err := filepath.Glob(filepath.Join(dir, "data", ".remove-alpha-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("trash directory should have been cleaned up after rollback: %v", matches)
	}
}

func TestProjectUpdateFlags(t *testing.T) {
	cfgPath := setupTestConfig(t, `[projects.alpha]
name = "Alpha"
github ="org/alpha"
`)

	_, _, err := execCmd(cfgPath, "update-project", "alpha", "--pypi", "alpha-pkg")
	if err != nil {
		t.Fatal(err)
	}

	content := readFileString(t, cfgPath)
	assertContains(t, content, `pypi = "alpha-pkg"`)
	assertContains(t, content, `github ="org/alpha"`)
}

func TestProjectUpdateUnset(t *testing.T) {
	cfgPath := setupTestConfig(t, `[projects.alpha]
name = "Alpha"
github ="org/alpha"
pypi = "alpha"
`)

	_, _, err := execCmd(cfgPath, "update-project", "alpha", "--unset", "pypi")
	if err != nil {
		t.Fatal(err)
	}

	content := readFileString(t, cfgPath)
	assertNotContains(t, content, "pypi")
}

func TestProjectUpdateNotFound(t *testing.T) {
	cfgPath := setupTestConfig(t, `[projects.alpha]
name = "Alpha"
github ="org/alpha"
`)

	_, _, err := execCmd(cfgPath, "update-project", "nonexistent", "--name", "New")
	if err == nil {
		t.Fatal("expected error for nonexistent project")
	}
}

func TestProjectShow(t *testing.T) {
	cfgPath := setupTestConfig(t, `[data]
dir = "data"

[projects.alpha]
name = "Alpha"
github ="org/alpha"
`)
	dir := filepath.Dir(cfgPath)

	dataDir := filepath.Join(dir, "data", "events", "github", "alpha")
	_ = os.MkdirAll(dataDir, 0755)
	writeTempFile(t, dataDir, "2025-01-01.jsonl", `{"source":"github","event_type":"star","project_id":"alpha","github_repo":"org/alpha","datetime":"2025-01-01T10:00:00Z","user":"alice"}`+"\n")
	writeTempFile(t, filepath.Join(dir, "data"), ".schema-version", currentSchemaVersion)

	_, buf, err := execCmd(cfgPath, "show-project", "alpha")
	if err != nil {
		t.Fatal(err)
	}

	output := buf.String()
	assertContains(t, output, "Alpha")
	assertContains(t, output, "2025-01-01")
}

func TestProjectShowUsesWatermarkForLastFetched(t *testing.T) {
	cfgPath := setupTestConfig(t, `[data]
dir = "data"

[projects.alpha]
name = "Alpha"
github = "org/alpha"
`)
	dir := filepath.Dir(cfgPath)

	metricsDir := filepath.Join(dir, "data", "metrics", "github", "alpha")
	_ = os.MkdirAll(metricsDir, 0755)
	// The total_stars value last changed on 2025-01-01...
	writeTempFile(t, metricsDir, "2025-01-01.jsonl",
		`{"source":"github","metric":"total_stars","project_id":"alpha","target":"org/alpha","date":"2025-01-01","value":100,"tags":{}}`+"\n")
	// ...but the source was successfully fetched (unchanged) through 2025-06-15.
	writeTempFile(t, metricsDir, "_watermark.json",
		`{"source":"github","project_id":"alpha","target":"org/alpha","date":"2025-06-15"}`+"\n")
	writeTempFile(t, filepath.Join(dir, "data"), ".schema-version", currentSchemaVersion)

	_, buf, err := execCmd(cfgPath, "show-project", "alpha")
	if err != nil {
		t.Fatal(err)
	}

	output := buf.String()
	assertContains(t, output, "last fetched: 2025-06-15")
	assertNotContains(t, output, "last fetched: 2025-01-01")
}

func TestProjectShowNotFound(t *testing.T) {
	cfgPath := setupTestConfig(t, `[projects.alpha]
name = "Alpha"
github ="org/alpha"
`)

	_, _, err := execCmd(cfgPath, "show-project", "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent project")
	}
}

func TestProjectShowJSON(t *testing.T) {
	cfgPath := setupTestConfig(t, `[data]
dir = "data"

[projects.alpha]
name = "Alpha"
github ="org/alpha"
`)

	_, buf, err := execCmd(cfgPath, "show-project", "alpha", "--json")
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if result["id"] != "alpha" {
		t.Errorf("id = %v, want 'alpha'", result["id"])
	}
}

func TestProjectRename(t *testing.T) {
	cfgPath := setupTestConfig(t, `[data]
dir = "data"

[projects.old-name]
name = "Old"
github ="org/old"
`)
	dir := filepath.Dir(cfgPath)

	dataDir := filepath.Join(dir, "data", "events", "github", "old-name")
	_ = os.MkdirAll(dataDir, 0755)
	writeTempFile(t, dataDir, "2025-01-01.jsonl", `{"source":"github","type":"star","project_id":"old-name","target":"org/old","datetime":"2025-01-01T00:00:00Z"}`+"\n")
	watermarkDir := filepath.Join(dir, "data", "metrics", "pypi", "old-name")
	_ = os.MkdirAll(watermarkDir, 0755)
	writeTempFile(t, watermarkDir, "_watermark.json", `{"source":"pypi","project_id":"old-name","target":"old-name","date":"2025-01-01"}`+"\n")
	writeTempFile(t, filepath.Join(dir, "data"), ".schema-version", currentSchemaVersion)

	_, _, err := execCmd(cfgPath, "rename-project", "old-name", "new-name")
	if err != nil {
		t.Fatal(err)
	}

	content := readFileString(t, cfgPath)
	assertNotContains(t, content, "old-name")
	assertContains(t, content, "[projects.new-name]")

	newDataDir := filepath.Join(dir, "data", "events", "github", "new-name")
	if _, err := os.Stat(newDataDir); os.IsNotExist(err) {
		t.Error("data directory not moved")
	}
	renamedData := readFileString(t, filepath.Join(newDataDir, "2025-01-01.jsonl"))
	assertContains(t, renamedData, `"project_id":"new-name"`)
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Error("old data directory still exists")
	}

	newWatermarkDir := filepath.Join(dir, "data", "metrics", "pypi", "new-name")
	if _, err := os.Stat(newWatermarkDir); os.IsNotExist(err) {
		t.Error("watermark directory not moved")
	}
	renamedWatermark := readFileString(t, filepath.Join(newWatermarkDir, "_watermark.json"))
	assertContains(t, renamedWatermark, `"project_id":"new-name"`)
	if _, err := os.Stat(watermarkDir); !os.IsNotExist(err) {
		t.Error("old watermark directory still exists")
	}
}

func TestProjectRenameNoMoveData(t *testing.T) {
	cfgPath := setupTestConfig(t, `[projects.old-name]
name = "Old"
github ="org/old"
`)

	_, _, err := execCmd(cfgPath, "rename-project", "old-name", "new-name", "--no-move-data")
	if err != nil {
		t.Fatal(err)
	}
}

func TestProjectRenameInvalidNewID(t *testing.T) {
	cfgPath := setupTestConfig(t, `[projects.alpha]
name = "Alpha"
github ="org/alpha"
`)

	_, _, err := execCmd(cfgPath, "rename-project", "alpha", "INVALID")
	if err == nil {
		t.Fatal("expected error for invalid new ID")
	}
}

func TestProjectRenameNewIDExists(t *testing.T) {
	cfgPath := setupTestConfig(t, `[projects.alpha]
name = "Alpha"
github ="org/alpha"

[projects.beta]
name = "Beta"
github ="org/beta"
`)

	_, _, err := execCmd(cfgPath, "rename-project", "alpha", "beta")
	if err == nil {
		t.Fatal("expected error when new ID already exists")
	}
}

func TestProjectRenameRewritesLogEntries(t *testing.T) {
	cfgPath := setupTestConfig(t, `[data]
dir = "data"

[projects.old-proj]
name = "Old"
pypi = "pkg"
`)
	dir := filepath.Dir(cfgPath)

	// Seed a metrics data directory so the rename has something to move.
	metricsDir := filepath.Join(dir, "data", "metrics", "pypi", "old-proj")
	if err := os.MkdirAll(metricsDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, metricsDir, "2025-06-01.jsonl",
		`{"source":"pypi","metric":"daily_downloads","project_id":"old-proj","target":"pkg","date":"2025-06-01","value":42}`+"\n")

	// Seed a log entry that references old-proj.
	logDir := filepath.Join(dir, "data", "log")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, logDir, "2025-06-01.jsonl",
		`{"run_id":"2025-06-01-01","timestamp":"2025-06-01T01:00:00Z","version":"v0.1.0","command":"fetch","source":"pypi","project_id":"old-proj","records":1}`+"\n")
	writeTempFile(t, filepath.Join(dir, "data"), ".schema-version", currentSchemaVersion)

	_, _, err := execCmd(cfgPath, "rename-project", "old-proj", "new-proj")
	if err != nil {
		t.Fatalf("rename-project failed: %v", err)
	}

	// The log file must now reference new-proj.
	logContent := readFileString(t, filepath.Join(logDir, "2025-06-01.jsonl"))
	assertContains(t, logContent, `"project_id":"new-proj"`)
	assertNotContains(t, logContent, `"project_id":"old-proj"`)
}

func TestRollbackProjectDataMovesReturnsError(t *testing.T) {
	dir := t.TempDir()
	// Create a move whose 'to' directory does not exist; RewriteProjectID will
	// fail when it tries to walk it, so rollback returns an error.
	moves := []projectDataMove{
		{
			from:  filepath.Join(dir, "src"),
			to:    filepath.Join(dir, "nonexistent"),
			oldID: "new-id",
			newID: "old-id",
		},
	}
	if err := rollbackProjectDataMoves(moves); err == nil {
		t.Fatal("expected rollbackProjectDataMoves to return error for non-existent path")
	}
}

func TestMoveProjectDataForRenameRollbackErrorPropagated(t *testing.T) {
	// Seed a metrics dir with invalid JSONL so RewriteProjectID fails.
	// Both the forward rewrite and the rollback rewrite fail on the same
	// file, producing a compound error.
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	oldMetrics := filepath.Join(dataDir, "metrics", "pypi", "old-id")
	if err := os.MkdirAll(oldMetrics, 0755); err != nil {
		t.Fatal(err)
	}
	// Invalid JSON triggers a rewrite error.
	if err := os.WriteFile(filepath.Join(oldMetrics, "2025-06-01.jsonl"),
		[]byte("{not valid json}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, _, err := moveProjectDataForRename(dataDir, "old-id", "new-id")
	if err == nil {
		t.Fatal("expected error from failed rewrite")
	}
	// The error should mention both the rewrite failure and rollback.
	if !strings.Contains(err.Error(), "rollback") {
		t.Errorf("expected compound error mentioning rollback, got: %v", err)
	}
}

func TestProjectImportFromJSON(t *testing.T) {
	cfgPath := setupTestConfig(t, `[data]
dir = "data"
`)
	dir := filepath.Dir(cfgPath)

	importFile := writeTempFile(t, dir, "projects.json", `[
		{"id": "proj-a", "github": "org/proj-a"},
		{"id": "proj-b", "github": "org/proj-b", "pypi": "proj-b"}
	]`)

	_, _, err := execCmd(cfgPath, "import-projects", "--from-file", importFile, "--yes")
	if err != nil {
		t.Fatal(err)
	}

	content := readFileString(t, cfgPath)
	assertContains(t, content, "[projects.proj-a]")
	assertContains(t, content, "[projects.proj-b]")
	assertContains(t, content, `pypi = "proj-b"`)
}

func TestProjectImportFromCSV(t *testing.T) {
	cfgPath := setupTestConfig(t, `[data]
dir = "data"
`)
	dir := filepath.Dir(cfgPath)

	importFile := writeTempFile(t, dir, "projects.csv", "id,name,github,pypi\nmy-proj,My Project,org/my-proj,my-proj\n")

	_, _, err := execCmd(cfgPath, "import-projects", "--from-file", importFile, "--yes")
	if err != nil {
		t.Fatal(err)
	}

	content := readFileString(t, cfgPath)
	assertContains(t, content, "[projects.my-proj]")
}

func TestProjectImportTrafficAliasFallback(t *testing.T) {
	cfgPath := setupTestConfig(t, `[data]
dir = "data"
`)
	dir := filepath.Dir(cfgPath)

	jsonFile := writeTempFile(t, dir, "projects.json", `[
		{"id": "json-proj", "github_traffic": "", "github-traffic": "org/json-proj"}
	]`)
	if _, _, err := execCmd(cfgPath, "import-projects", "--from-file", jsonFile, "--yes"); err != nil {
		t.Fatal(err)
	}

	csvFile := writeTempFile(t, dir, "projects.csv", "id,github-traffic,github_traffic\ncsv-proj, ,org/csv-proj\n")
	if _, _, err := execCmd(cfgPath, "import-projects", "--from-file", csvFile, "--yes"); err != nil {
		t.Fatal(err)
	}

	content := readFileString(t, cfgPath)
	assertContains(t, content, `github-traffic = "org/json-proj"`)
	assertContains(t, content, `github-traffic = "org/csv-proj"`)
}

func TestProjectImportDryRun(t *testing.T) {
	cfgPath := setupTestConfig(t, `[data]
dir = "data"
`)
	dir := filepath.Dir(cfgPath)

	importFile := writeTempFile(t, dir, "projects.json", `[{"id": "proj-a", "github": "org/proj-a"}]`)

	_, _, err := execCmd(cfgPath, "import-projects", "--from-file", importFile, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}

	content := readFileString(t, cfgPath)
	assertNotContains(t, content, "proj-a")
}

func TestProjectImportSkipExisting(t *testing.T) {
	cfgPath := setupTestConfig(t, `[data]
dir = "data"

[projects.existing]
name = "Existing"
github ="org/existing"
`)
	dir := filepath.Dir(cfgPath)

	importFile := writeTempFile(t, dir, "projects.json", `[
		{"id": "existing", "github": "org/existing"},
		{"id": "new-proj", "github": "org/new-proj"}
	]`)

	_, _, err := execCmd(cfgPath, "import-projects", "--from-file", importFile, "--skip-existing", "--yes")
	if err != nil {
		t.Fatal(err)
	}

	content := readFileString(t, cfgPath)
	assertContains(t, content, "[projects.new-proj]")
}

func TestProjectInitCreatesConfig(t *testing.T) {
	dir := t.TempDir()

	rootCmd := newRootCmd()
	rootCmd.SetArgs([]string{"init", "--dir", dir})
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(dir, "velocirepo.toml")
	content := readFileString(t, cfgPath)
	assertContains(t, content, `dir = "velocirepo/data"`)

	dataDir := filepath.Join(dir, "velocirepo/data")
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		t.Error("data directory not created")
	}
}

func TestProjectInitAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "velocirepo.toml", "[data]\n")

	rootCmd := newRootCmd()
	rootCmd.SetArgs([]string{"init", "--dir", dir})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when config already exists")
	}
}
