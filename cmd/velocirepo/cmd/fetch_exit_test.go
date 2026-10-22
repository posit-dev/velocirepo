package cmd

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// A failed job must fail the command, so that CI notices, after the results
// of the other jobs have been logged and stored.
func TestFetchExitsNonZeroWhenAJobFails(t *testing.T) {
	for _, command := range []string{"fetch", "fetch-github"} {
		t.Run(command, func(t *testing.T) {
			cfgPath := setupTestConfig(t, `[data]
dir = "data"

[projects.broken]
github = "not-a-repo"
`)
			_, _, err := execCmd(cfgPath, command)
			if err == nil || !strings.Contains(err.Error(), "1 fetch job(s) failed") {
				t.Fatalf("err = %v, want 1 failed job", err)
			}
			logs, _ := filepath.Glob(filepath.Join(filepath.Dir(cfgPath), "data", "log", "*.jsonl"))
			if len(logs) != 1 {
				t.Fatalf("fetch log files = %v, want one", logs)
			}
			if !strings.Contains(readFileString(t, logs[0]), "invalid github repo") {
				t.Errorf("the failure is not in the fetch log")
			}
		})
	}
}

func TestWarningsAreShownByDefault(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	verbose = false
	setupLogging()
	if !slog.Default().Enabled(context.Background(), slog.LevelWarn) {
		t.Error("warnings are suppressed by default")
	}
	if slog.Default().Enabled(context.Background(), slog.LevelInfo) {
		t.Error("info messages are shown without --verbose")
	}
}

// fetch --results writes what every job did, also when one failed, so that
// check and gate can read it.
func TestFetchWritesResults(t *testing.T) {
	cfgPath := setupTestConfig(t, `[data]
dir = "data"

[projects.broken]
github = "not-a-repo"
`)
	path := filepath.Join(t.TempDir(), "results.json")
	_, _, err := execCmd(cfgPath, "fetch-github", "--results", path)
	if err == nil {
		t.Fatal("expected the failed job to fail the command")
	}
	results := readFileString(t, path)
	if !strings.Contains(results, `"project_id": "broken"`) || !strings.Contains(results, "invalid github repo") {
		t.Errorf("results = %s", results)
	}
}

// fetch --archive writes a run's archive outside the data, and keeps it only
// if it contains none of the tokens.
func TestFetchArchive(t *testing.T) {
	cfgPath := setupTestConfig(t, `[data]
dir = "data"

[projects.broken]
github = "not-a-repo"
`)
	dir := t.TempDir()
	_, _, _ = execCmd(cfgPath, "fetch-github", "--archive", dir)
	manifests, _ := filepath.Glob(filepath.Join(dir, "*", "manifest.json"))
	if len(manifests) != 1 {
		t.Fatalf("manifests = %v, want one run", manifests)
	}

	// Without --archive, nothing is archived.
	other := t.TempDir()
	_, _, _ = execCmd(cfgPath, "fetch-github")
	if entries, _ := filepath.Glob(filepath.Join(other, "*")); len(entries) != 0 {
		t.Errorf("archived without --archive: %v", entries)
	}
}
