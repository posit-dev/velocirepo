package archive

import (
	"path/filepath"
	"testing"
	"time"
)

// A repair range selects every run whose end dates fall in it, oldest first.
func TestRunsInRange(t *testing.T) {
	dir := t.TempDir()
	for i, run := range []struct {
		id  string
		end string
		at  time.Time
	}{
		{"c", "2026-10-12", time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)},
		{"a", "2026-10-10", time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)},
		{"b", "2026-10-20", time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)},
	} {
		w, err := NewWriter(dir, run.id, "test", run.at)
		if err != nil {
			t.Fatal(err)
		}
		w.AddJob(Job{Source: "pypi", Project: "proj", Target: "pkg", Start: map[string]string{"": run.end}, End: map[string]string{"": run.end}})
		if i == 0 {
			w.AddJob(Job{Source: "homebrew", Project: "other", Target: "pkg", End: map[string]string{"": run.end}})
		}
		if _, err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	got, err := RunsInRange(dir, "pypi", "proj", "2026-10-01", "2026-10-15")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || filepath.Base(got[0]) != "a" || filepath.Base(got[1]) != "c" {
		t.Errorf("runs = %v, want a and c, oldest first", got)
	}
	if got, _ := RunsInRange(dir, "homebrew", "proj", "2026-10-01", "2026-10-31"); len(got) != 0 {
		t.Errorf("runs of another project: %v", got)
	}
}
