package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/testutil"
)

func TestHandleUpdateProjectReloadsConfig(t *testing.T) {
	cfgPath := testutil.WriteConfig(t, `[projects.alpha]
name = "Alpha"
github = "org/alpha"
`)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	h := &handlers{cfg: cfg}

	result, err := h.handleUpdateProject(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Arguments: map[string]any{
				"id":       "alpha",
				"linkedin": "urn:li:organization:123",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("handleUpdateProject returned error result: %#v", result.Content)
	}

	proj, err := h.cfg.GetProject("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got := []string(proj.LinkedIn); len(got) != 1 || got[0] != "urn:li:organization:123" {
		t.Fatalf("h.cfg LinkedIn = %#v, want urn:li:organization:123", got)
	}
}

func TestResolveExportDirRejectsAbsolutePath(t *testing.T) {
	cfgPath := testutil.WriteConfig(t, "")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	h := &handlers{cfg: cfg}

	_, err = h.resolveExportDir("/tmp/evil")
	if err == nil {
		t.Fatal("expected error for absolute path, got nil")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error = %q, want substring %q", err, "absolute")
	}
}

func TestResolveExportDirRejectsDotDotEscape(t *testing.T) {
	cfgPath := testutil.WriteConfig(t, "")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	h := &handlers{cfg: cfg}

	_, err = h.resolveExportDir("../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for .. escape, got nil")
	}
}

func TestResolveExportDirAcceptsSubdirectory(t *testing.T) {
	cfgPath := testutil.WriteConfig(t, "")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	h := &handlers{cfg: cfg}

	got, err := h.resolveExportDir("exports/parquet")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	projectDir := filepath.Dir(cfgPath)
	want := filepath.Join(projectDir, "exports", "parquet")
	if got != want {
		t.Errorf("resolveExportDir = %q, want %q", got, want)
	}
}

func TestResolveExportDirRejectsSymlinkEscape(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()

	cfgPath := testutil.WriteTempFile(t, projectDir, "velocirepo.toml", "")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	h := &handlers{cfg: cfg}

	// Create a symlink inside the project dir that points outside.
	linkPath := filepath.Join(projectDir, "link-to-outside")
	if err := os.Symlink(outsideDir, linkPath); err != nil {
		t.Skip("cannot create symlinks:", err)
	}

	_, err = h.resolveExportDir("link-to-outside")
	if err == nil {
		t.Fatal("expected error for symlink escaping project directory, got nil")
	}
}
