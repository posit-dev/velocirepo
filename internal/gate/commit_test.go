package gate

import (
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/fetch"
)

func TestDataMessage(t *testing.T) {
	got := dataMessage(time.Date(2026, 10, 21, 9, 31, 0, 0, time.UTC), []fetch.TargetFreshness{
		{Source: "pypi", Target: "a", Watermark: "2026-10-20"},
		{Source: "github", Target: "x", Watermark: "2026-10-20"},
		{Source: "github", Target: "x", Series: "stars", Watermark: "2026-10-19"},
		{Source: "cran", Target: "b", Watermark: "2026-10-19"},
		{Source: "cran", Target: "c", Watermark: "2026-10-18"},
	}, nil)
	want := "Fetch metrics on 2026-10-21\n\nStored through:\n- github, pypi: 2026-10-20\n- cran: 2026-10-18\n"
	if got != want {
		t.Errorf("message =\n%s\nwant\n%s", got, want)
	}
}

func TestConcatenationMessage(t *testing.T) {
	for _, c := range []struct {
		added []string
		want  string
	}{
		{[]string{"velocirepo/data/metrics/pypi/p/2026-10.jsonl", "velocirepo/data/events/github/p/2026-10.jsonl"}, "Concatenate October 2026\n"},
		{[]string{"m/2025-12.jsonl", "m/2025.jsonl"}, "Concatenate December 2025 and 2025\n"},
	} {
		if got := concatenationMessage(c.added); got != c.want {
			t.Errorf("%v: %q, want %q", c.added, got, c.want)
		}
	}
}
