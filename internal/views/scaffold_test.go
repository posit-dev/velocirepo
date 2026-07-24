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

func TestScaffoldRUsesIr(t *testing.T) {
	viewsDir := t.TempDir()

	dir, err := Scaffold(ScaffoldOptions{
		ViewsDir:  viewsDir,
		Name:      "r-view",
		Framework: FrameworkR,
		Source:    "duckdb",
		DBPath:    "../../data/velocirepo.duckdb",
	})
	if err != nil {
		t.Fatal(err)
	}

	// ir bootstraps its own environment, so no renv scaffolding is written.
	if _, err := os.Stat(filepath.Join(dir, ".Rprofile")); !os.IsNotExist(err) {
		t.Error(".Rprofile should not be created; ir manages the environment")
	}
	if _, err := os.Stat(filepath.Join(dir, "renv")); !os.IsNotExist(err) {
		t.Error("renv/ should not be created; ir manages the environment")
	}

	renderSh, _ := os.ReadFile(filepath.Join(dir, "render.sh"))
	if !strings.Contains(string(renderSh), "ir run view.R") {
		t.Errorf("render.sh should run the view via ir; got: %s", renderSh)
	}

	// The view declares its packages inline for ir to resolve.
	view, _ := os.ReadFile(filepath.Join(dir, "view.R"))
	if !strings.Contains(string(view), "#| packages:") {
		t.Errorf("view.R should declare ir packages in #| frontmatter; got: %s", view)
	}

	// R frameworks do not use pyproject.toml.
	if _, err := os.Stat(filepath.Join(dir, "pyproject.toml")); !os.IsNotExist(err) {
		t.Error("pyproject.toml should not be created for R framework")
	}
}

func TestScaffoldQuartoRUsesIr(t *testing.T) {
	viewsDir := t.TempDir()

	dir, err := Scaffold(ScaffoldOptions{
		ViewsDir:  viewsDir,
		Name:      "qr-view",
		Framework: FrameworkQuartoR,
		Source:    "duckdb",
		DBPath:    "../../data/velocirepo.duckdb",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, ".Rprofile")); !os.IsNotExist(err) {
		t.Error(".Rprofile should not be created; ir manages the environment")
	}
	if _, err := os.Stat(filepath.Join(dir, "renv")); !os.IsNotExist(err) {
		t.Error("renv/ should not be created; ir manages the environment")
	}

	renderSh, _ := os.ReadFile(filepath.Join(dir, "render.sh"))
	if !strings.Contains(string(renderSh), "ir render view.qmd") {
		t.Errorf("render.sh should render the view via ir; got: %s", renderSh)
	}

	// The view declares its packages in the ir: block of the YAML header.
	view, _ := os.ReadFile(filepath.Join(dir, "view.qmd"))
	if !strings.Contains(string(view), "ir:") {
		t.Errorf("view.qmd should declare an ir: block; got: %s", view)
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
