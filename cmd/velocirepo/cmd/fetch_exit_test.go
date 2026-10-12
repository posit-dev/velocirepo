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
