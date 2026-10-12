package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

// setupBadgeConfig creates a config file pointing at a temp data dir that
// already has a .schema-version marker so schema checks pass.
func setupBadgeConfig(t *testing.T) (cfgPath, dataDir string) {
	t.Helper()
	dir := t.TempDir()
	dataDir = filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, ".schema-version"),
		[]byte(fmt.Sprintf("%d\n", store.LatestSchemaVersion)), 0644); err != nil {
		t.Fatal(err)
	}
	cfgPath = filepath.Join(dir, "velocirepo.toml")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf(
		"[data]\ndir = %q\n\n[projects.proj-a]\nname = \"Proj A\"\npypi = \"pkg-a\"\n\n[projects.proj-b]\nname = \"Proj B\"\npypi = \"pkg-b\"\n",
		dataDir,
	)), 0644); err != nil {
		t.Fatal(err)
	}
	return cfgPath, dataDir
}

func TestBadgeDownloadsPreset(t *testing.T) {
	cfgPath, dataDir := setupBadgeConfig(t)

	records := []source.Record{
		{Metric: "daily_downloads", ProjectID: "proj-a", Target: "pkg-a", Date: "2025-06-01", Value: 100},
		{Metric: "daily_downloads", ProjectID: "proj-a", Target: "pkg-a", Date: "2025-06-02", Value: 200},
	}
	if _, err := store.WriteRecords(dataDir, "pypi", "proj-a", records); err != nil {
		t.Fatal(err)
	}

	_, buf, err := execCmd(cfgPath, "badge", "downloads")
	if err != nil {
		t.Fatalf("badge downloads: %v\n%s", err, buf.String())
	}
	svg := buf.String()
	// Total downloads = 100 + 200 = 300
	assertContains(t, svg, "300")
}

func TestBadgeDownloadsPresetNoData(t *testing.T) {
	cfgPath, _ := setupBadgeConfig(t)

	_, buf, err := execCmd(cfgPath, "badge", "downloads")
	if err != nil {
		t.Fatalf("badge downloads with no data: %v\n%s", err, buf.String())
	}
	svg := buf.String()
	// No data → COALESCE returns 0, never <nil>
	assertNotContains(t, svg, "<nil>")
	assertContains(t, svg, "0")
}

func TestBadgePageviewsPreset(t *testing.T) {
	cfgPath, dataDir := setupBadgeConfig(t)

	records := []source.Record{
		{Metric: "daily_site_pageviews", ProjectID: "proj-a", Target: "posit.co", Date: "2025-06-01", Value: 50},
		{Metric: "daily_site_pageviews", ProjectID: "proj-a", Target: "posit.co", Date: "2025-06-02", Value: 70},
	}
	if _, err := store.WriteRecords(dataDir, "plausible", "proj-a", records); err != nil {
		t.Fatal(err)
	}

	_, buf, err := execCmd(cfgPath, "badge", "pageviews")
	if err != nil {
		t.Fatalf("badge pageviews: %v\n%s", err, buf.String())
	}
	// Total pageviews = 50 + 70 = 120
	assertContains(t, buf.String(), "120")
}

func TestBadgeDownloadsProjectFilter(t *testing.T) {
	cfgPath, dataDir := setupBadgeConfig(t)

	// proj-a: 300, proj-b: 1000; with --project proj-a must show only 300.
	recordsA := []source.Record{
		{Metric: "daily_downloads", ProjectID: "proj-a", Target: "pkg-a", Date: "2025-06-01", Value: 300},
	}
	recordsB := []source.Record{
		{Metric: "daily_downloads", ProjectID: "proj-b", Target: "pkg-b", Date: "2025-06-01", Value: 1000},
	}
	if _, err := store.WriteRecords(dataDir, "pypi", "proj-a", recordsA); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteRecords(dataDir, "pypi", "proj-b", recordsB); err != nil {
		t.Fatal(err)
	}

	_, buf, err := execCmd(cfgPath, "badge", "downloads", "--project", "proj-a")
	if err != nil {
		t.Fatalf("badge downloads --project: %v\n%s", err, buf.String())
	}
	svg := buf.String()
	assertContains(t, svg, "300")
	// proj-b's 1000 must not appear in the scoped badge.
	if strings.Contains(svg, "1.3k") || strings.Contains(svg, "1000") {
		t.Errorf("badge with --project proj-a shows proj-b data: %s", svg)
	}
}

func TestBadgeStarsPreset(t *testing.T) {
	cfgPath, dataDir := setupBadgeConfig(t)

	events := []source.Event{
		{Type: "star", ProjectID: "proj-a", Target: "org/repo", Datetime: "2025-06-01T10:00:00Z", User: "alice"},
		{Type: "star", ProjectID: "proj-a", Target: "org/repo", Datetime: "2025-06-02T10:00:00Z", User: "bob"},
		{Type: "fork", ProjectID: "proj-a", Target: "org/repo", Datetime: "2025-06-01T10:00:00Z", User: "carol"},
	}
	if _, err := store.WriteEvents(dataDir, "github", "proj-a", events); err != nil {
		t.Fatal(err)
	}

	_, buf, err := execCmd(cfgPath, "badge", "stars")
	if err != nil {
		t.Fatalf("badge stars: %v\n%s", err, buf.String())
	}
	assertContains(t, buf.String(), "2")
}
