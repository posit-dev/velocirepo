package views

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScaffold(t *testing.T) {
	tests := []struct {
		name     string
		fw       Framework
		source   string
		contains string
		file     string
	}{
		{"stars", FrameworkQuartoPython, "duckdb", "duckdb.connect", "view.qmd"},
		{"stars", FrameworkQuartoPython, "parquet", "read_parquet", "view.qmd"},
		{"stars-r", FrameworkQuartoR, "duckdb", "dbConnect(duckdb()", "view.qmd"},
		{"stars-r", FrameworkQuartoR, "parquet", "read_parquet", "view.qmd"},
		{"overview", FrameworkMarimo, "duckdb", "duckdb.connect", "app.py"},
		{"overview", FrameworkMarimo, "parquet", "read_parquet", "app.py"},
		{"analysis", FrameworkJupyter, "duckdb", "duckdb.connect", "view.ipynb"},
		{"analysis", FrameworkJupyter, "parquet", "read_parquet", "view.ipynb"},
		{"trend", FrameworkR, "duckdb", "dbConnect(duckdb()", "view.R"},
		{"trend", FrameworkR, "parquet", "read_parquet", "view.R"},
		{"badge", FrameworkSQL, "duckdb", ".duckdb", "view.sql"},
		{"badge", FrameworkSQL, "parquet", "read_parquet", "view.sql"},
	}

	for _, tt := range tests {
		t.Run(string(tt.fw)+"_"+tt.source, func(t *testing.T) {
			viewsDir := t.TempDir()

			dir, err := Scaffold(ScaffoldOptions{
				ViewsDir:  viewsDir,
				Name:      tt.name,
				Framework: tt.fw,
				Source:    tt.source,
				DBPath:    "../../data/velocirepo.duckdb",
				DataDir:   "../_data",
			})
			if err != nil {
				t.Fatal(err)
			}

			viewFile := filepath.Join(dir, tt.file)
			content, err := os.ReadFile(viewFile)
			if err != nil {
				t.Fatalf("view file not created: %v", err)
			}

			if !strings.Contains(string(content), tt.contains) {
				t.Errorf("view file does not contain %q:\n%s", tt.contains, content)
			}

			renderSh := filepath.Join(dir, "render.sh")
			info, err := os.Stat(renderSh)
			if err != nil {
				t.Fatal("render.sh not created")
			}
			if info.Mode().Perm()&0100 == 0 {
				t.Error("render.sh is not executable")
			}
		})
	}
}

func TestScaffoldPyproject(t *testing.T) {
	for _, fw := range []Framework{FrameworkQuartoPython, FrameworkJupyter, FrameworkMarimo} {
		t.Run(string(fw), func(t *testing.T) {
			viewsDir := t.TempDir()

			dir, err := Scaffold(ScaffoldOptions{
				ViewsDir:  viewsDir,
				Name:      "test",
				Framework: fw,
				Source:    "duckdb",
				DBPath:    "../../data/velocirepo.duckdb",
			})
			if err != nil {
				t.Fatal(err)
			}

			pyproject := filepath.Join(dir, "pyproject.toml")
			if _, err := os.Stat(pyproject); err != nil {
				t.Error("pyproject.toml not created for Python framework")
			}
		})
	}
}

func TestScaffoldNoUV(t *testing.T) {
	viewsDir := t.TempDir()

	dir, err := Scaffold(ScaffoldOptions{
		ViewsDir:  viewsDir,
		Name:      "test",
		Framework: FrameworkQuartoPython,
		Source:    "duckdb",
		DBPath:    "../../data/velocirepo.duckdb",
		NoUV:      true,
	})
	if err != nil {
		t.Fatal(err)
	}

	pyproject := filepath.Join(dir, "pyproject.toml")
	if _, err := os.Stat(pyproject); !os.IsNotExist(err) {
		t.Error("pyproject.toml should not be created with --no-uv")
	}
}

func TestScaffoldRenv(t *testing.T) {
	viewsDir := t.TempDir()

	dir, err := Scaffold(ScaffoldOptions{
		ViewsDir:  viewsDir,
		Name:      "r-view",
		Framework: FrameworkR,
		Source:    "duckdb",
		DBPath:    "../../data/velocirepo.duckdb",
		Renv:      true,
	})
	if err != nil {
		t.Fatal(err)
	}

	rprofile := filepath.Join(dir, ".Rprofile")
	rprofileContent, err := os.ReadFile(rprofile)
	if err != nil {
		t.Error(".Rprofile not created")
	}
	// The source() must be guarded: activate.R does not exist until the first
	// render runs renv::activate(), and an unguarded source() aborts every
	// Rscript (including render.sh's own bootstrap) before it can be created.
	if !strings.Contains(string(rprofileContent), `file.exists("renv/activate.R")`) {
		t.Errorf(".Rprofile must guard the source() with file.exists; got: %s", rprofileContent)
	}

	renvSettings := filepath.Join(dir, "renv", "settings.json")
	if _, err := os.Stat(renvSettings); err != nil {
		t.Error("renv/settings.json not created")
	}

	renderSh := filepath.Join(dir, "render.sh")
	content, _ := os.ReadFile(renderSh)
	if !strings.Contains(string(content), "renv::activate") {
		t.Error("render.sh should activate renv to generate renv/activate.R")
	}
	if !strings.Contains(string(content), "renv::restore") {
		t.Error("render.sh should contain renv::restore")
	}
}

func TestScaffoldRNoRenv(t *testing.T) {
	viewsDir := t.TempDir()

	dir, err := Scaffold(ScaffoldOptions{
		ViewsDir:  viewsDir,
		Name:      "r-plain",
		Framework: FrameworkR,
		Source:    "duckdb",
		DBPath:    "../../data/velocirepo.duckdb",
	})
	if err != nil {
		t.Fatal(err)
	}

	rprofile := filepath.Join(dir, ".Rprofile")
	if _, err := os.Stat(rprofile); !os.IsNotExist(err) {
		t.Error(".Rprofile should not exist without --renv")
	}

	// Should NOT have pyproject.toml for R
	pyproject := filepath.Join(dir, "pyproject.toml")
	if _, err := os.Stat(pyproject); !os.IsNotExist(err) {
		t.Error("pyproject.toml should not be created for R framework")
	}
}

func TestScaffoldQuartoRRenv(t *testing.T) {
	viewsDir := t.TempDir()

	dir, err := Scaffold(ScaffoldOptions{
		ViewsDir:  viewsDir,
		Name:      "qr-view",
		Framework: FrameworkQuartoR,
		Source:    "duckdb",
		DBPath:    "../../data/velocirepo.duckdb",
		Renv:      true,
	})
	if err != nil {
		t.Fatal(err)
	}

	rprofileContent, err := os.ReadFile(filepath.Join(dir, ".Rprofile"))
	if err != nil {
		t.Error(".Rprofile not created")
	}
	if !strings.Contains(string(rprofileContent), `file.exists("renv/activate.R")`) {
		t.Errorf(".Rprofile must guard the source() with file.exists; got: %s", rprofileContent)
	}
	if _, err := os.Stat(filepath.Join(dir, "renv", "settings.json")); err != nil {
		t.Error("renv/settings.json not created")
	}

	renderSh, _ := os.ReadFile(filepath.Join(dir, "render.sh"))
	if !strings.Contains(string(renderSh), "renv::activate") {
		t.Error("render.sh should activate renv to generate renv/activate.R")
	}
	if !strings.Contains(string(renderSh), "renv::restore") {
		t.Error("render.sh should contain renv::restore")
	}
	if !strings.Contains(string(renderSh), "quarto render") {
		t.Error("render.sh should render with quarto")
	}

	// quarto-r is an R framework: no pyproject.toml.
	if _, err := os.Stat(filepath.Join(dir, "pyproject.toml")); !os.IsNotExist(err) {
		t.Error("pyproject.toml should not be created for quarto-r framework")
	}
}

func TestScaffoldSubdirectory(t *testing.T) {
	viewsDir := t.TempDir()

	dir, err := Scaffold(ScaffoldOptions{
		ViewsDir:  viewsDir,
		Name:      "weekly/stars",
		Framework: FrameworkQuartoPython,
		Source:    "duckdb",
		DBPath:    "../../../data/velocirepo.duckdb",
	})
	if err != nil {
		t.Fatal(err)
	}

	expected := filepath.Join(viewsDir, "weekly", "stars")
	if dir != expected {
		t.Errorf("dir = %q, want %q", dir, expected)
	}

	if _, err := os.Stat(filepath.Join(dir, "render.sh")); err != nil {
		t.Error("render.sh not created in subdirectory")
	}
}

func TestScaffoldAlreadyExists(t *testing.T) {
	viewsDir := t.TempDir()

	_ = os.MkdirAll(filepath.Join(viewsDir, "existing"), 0755)

	_, err := Scaffold(ScaffoldOptions{
		ViewsDir:  viewsDir,
		Name:      "existing",
		Framework: FrameworkQuartoPython,
		Source:    "duckdb",
		DBPath:    "../../data/velocirepo.duckdb",
	})
	if err == nil {
		t.Error("expected error for existing directory")
	}
}

func TestScaffoldRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	viewsDir := filepath.Join(root, "views")

	_, err := Scaffold(ScaffoldOptions{
		ViewsDir:  viewsDir,
		Name:      "../outside",
		Framework: FrameworkQuartoPython,
		Source:    "duckdb",
		DBPath:    "../../data/velocirepo.duckdb",
	})
	if err == nil {
		t.Fatal("expected error for path traversal")
	}

	outside := filepath.Join(root, "outside")
	if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
		t.Fatalf("outside directory was created: %v", statErr)
	}
}

func TestScaffoldRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	viewsDir := filepath.Join(root, "views")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(viewsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(viewsDir, "linked")); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	_, err := Scaffold(ScaffoldOptions{
		ViewsDir:  viewsDir,
		Name:      "linked/new",
		Framework: FrameworkQuartoPython,
		Source:    "duckdb",
		DBPath:    "../../data/velocirepo.duckdb",
	})
	if err == nil {
		t.Fatal("expected error for symlink escape")
	}

	if _, statErr := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(statErr) {
		t.Fatalf("outside directory was created: %v", statErr)
	}
}

func TestScaffoldRejectsAbsoluteName(t *testing.T) {
	viewsDir := t.TempDir()

	_, err := Scaffold(ScaffoldOptions{
		ViewsDir:  viewsDir,
		Name:      filepath.Join(viewsDir, "absolute"),
		Framework: FrameworkQuartoPython,
		Source:    "duckdb",
		DBPath:    "../../data/velocirepo.duckdb",
	})
	if err == nil {
		t.Fatal("expected error for absolute view name")
	}
}
