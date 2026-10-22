package archive

import (
	"strings"
	"testing"
	"time"
)

func TestRunIDs(t *testing.T) {
	at := time.Date(2026, 10, 21, 9, 31, 5, 0, time.UTC)

	ci := NewRunID(at, func(k string) string {
		return map[string]string{"GITHUB_RUN_ID": "39552828008", "GITHUB_RUN_ATTEMPT": "2"}[k]
	})
	if ci != "20261021T093105Z-39552828008-2" {
		t.Errorf("CI run ID = %q", ci)
	}

	a := NewRunID(at, func(string) string { return "" })
	b := NewRunID(at, func(string) string { return "" })
	if !strings.HasPrefix(a, "20261021T093105Z-local-") || a == b {
		t.Errorf("local run IDs %q and %q should be distinct and marked local", a, b)
	}
}

func TestAssetNames(t *testing.T) {
	if got := AssetName("20261021T093105Z-1-1", "github"); got != "20261021T093105Z-1-1-github.jsonl.zst" {
		t.Errorf("AssetName = %q", got)
	}
	if got := ManifestAssetName("20261021T093105Z-1-1"); got != "20261021T093105Z-1-1-manifest.json" {
		t.Errorf("ManifestAssetName = %q", got)
	}
	if got := ReleaseTag(time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)); got != "raw-2026-10" {
		t.Errorf("ReleaseTag = %q", got)
	}
}
