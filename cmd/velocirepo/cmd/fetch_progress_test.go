package cmd

import (
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/fetch"
)

func TestRunningLine(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	got := runningLine([]fetch.RunningJob{
		{Source: "github", Project: "sparklyr", Since: now.Add(-12*time.Minute - 3*time.Second)},
		{Source: "github", Project: "readxl", Since: now.Add(-90 * time.Second)},
	}, 812, 1654, now)
	want := " [812/1654] … running: sparklyr/github 12m3s, readxl/github 1m30s"
	if got != want {
		t.Errorf("runningLine = %q, want %q", got, want)
	}
}
