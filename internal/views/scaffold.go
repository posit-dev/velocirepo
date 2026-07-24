package views

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed templates/*
var templateFS embed.FS

type Framework string

const (
	FrameworkQuartoPython Framework = "quarto-python"
	FrameworkQuartoR      Framework = "quarto-r"
	FrameworkJupyter      Framework = "jupyter"
	FrameworkMarimo       Framework = "marimo"
	FrameworkR            Framework = "r"
	FrameworkSQL          Framework = "sql"
)

func ParseFramework(s string) (Framework, error) {
	switch strings.ToLower(s) {
	case "quarto-python":
		return FrameworkQuartoPython, nil
	case "quarto-r":
		return FrameworkQuartoR, nil
	case "jupyter":
		return FrameworkJupyter, nil
	case "marimo":
		return FrameworkMarimo, nil
	case "r":
		return FrameworkR, nil
	case "sql":
		return FrameworkSQL, nil
	default:
		return "", fmt.Errorf("unknown framework %q (available: quarto-python, quarto-r, jupyter, marimo, r, sql)", s)
	}
}

type ScaffoldOptions struct {
	ViewsDir  string
	Name      string
	Framework Framework
	Source    string
	DBPath    string
	DataDir   string
	NoUV      bool
}

type scaffoldData struct {
	ViewName string
	DBPath   string
	DataDir  string
}

func Scaffold(opts ScaffoldOptions) (string, error) {
	dir, err := ScaffoldDir(opts.ViewsDir, opts.Name)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(dir); err == nil {
		return "", fmt.Errorf("view directory %q already exists", dir)
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create view directory: %w", err)
	}

	data := scaffoldData{
		ViewName: filepath.Base(opts.Name),
		DBPath:   opts.DBPath,
		DataDir:  opts.DataDir,
	}

	renderTmpl := fmt.Sprintf("templates/%s/render.sh.tmpl", opts.Framework)
	if err := writeTemplate(dir, "render.sh", renderTmpl, data, 0755); err != nil {
		return "", err
	}

	viewFile, viewTmpl := viewFileTemplate(opts.Framework, opts.Source)
	if err := writeTemplate(dir, viewFile, viewTmpl, data, 0644); err != nil {
		return "", err
	}

	if serveTmpl := serveShTemplate(opts.Framework); serveTmpl != "" {
		if err := writeTemplate(dir, "serve.sh", serveTmpl, data, 0755); err != nil {
			return "", err
		}
	}

	if needsPyproject(opts.Framework) && !opts.NoUV {
		pyTmpl := fmt.Sprintf("templates/%s/pyproject.toml.tmpl", opts.Framework)
		if err := writeTemplate(dir, "pyproject.toml", pyTmpl, data, 0644); err != nil {
			return "", err
		}
	}

	return dir, nil
}

func ScaffoldDir(viewsDir, name string) (string, error) {
	cleanName := filepath.Clean(name)
	if cleanName == "." || !filepath.IsLocal(cleanName) {
		return "", fmt.Errorf("invalid view name %q: must be a relative path inside the views directory", name)
	}

	absViewsDir, err := filepath.Abs(viewsDir)
	if err != nil {
		return "", fmt.Errorf("resolve views directory: %w", err)
	}
	dir := filepath.Join(absViewsDir, cleanName)
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve view directory: %w", err)
	}
	rel, err := filepath.Rel(absViewsDir, absDir)
	if err != nil {
		return "", fmt.Errorf("check view directory: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("invalid view name %q: must stay inside %s", name, absViewsDir)
	}
	if err := validateNoSymlinkEscape(absViewsDir, absDir); err != nil {
		return "", err
	}
	return absDir, nil
}

func validateNoSymlinkEscape(absViewsDir, absDir string) error {
	resolvedViewsDir, err := filepath.EvalSymlinks(absViewsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("resolve views directory symlinks: %w", err)
	}
	resolvedViewsDir, err = filepath.Abs(resolvedViewsDir)
	if err != nil {
		return fmt.Errorf("resolve views directory: %w", err)
	}

	rel, err := filepath.Rel(absViewsDir, absDir)
	if err != nil {
		return fmt.Errorf("check view directory symlinks: %w", err)
	}
	current := absViewsDir
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("inspect view path %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}

		resolved, err := filepath.EvalSymlinks(current)
		if err != nil {
			return fmt.Errorf("resolve view path symlink %s: %w", current, err)
		}
		resolved, err = filepath.Abs(resolved)
		if err != nil {
			return fmt.Errorf("resolve view path %s: %w", current, err)
		}
		if !pathInside(resolvedViewsDir, resolved) {
			return fmt.Errorf("invalid view name: symlink %q points outside views directory", current)
		}
	}
	return nil
}

func pathInside(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func serveShTemplate(fw Framework) string {
	switch fw {
	case FrameworkQuartoPython, FrameworkQuartoR, FrameworkJupyter, FrameworkMarimo:
		return fmt.Sprintf("templates/%s/serve.sh.tmpl", fw)
	default:
		return ""
	}
}

func viewFileTemplate(fw Framework, source string) (filename, tmplPath string) {
	switch fw {
	case FrameworkQuartoPython:
		return "view.qmd", fmt.Sprintf("templates/quarto-python/view.qmd.%s.tmpl", source)
	case FrameworkQuartoR:
		return "view.qmd", fmt.Sprintf("templates/quarto-r/view.qmd.%s.tmpl", source)
	case FrameworkJupyter:
		return "view.ipynb", fmt.Sprintf("templates/jupyter/view.ipynb.%s.tmpl", source)
	case FrameworkMarimo:
		return "app.py", fmt.Sprintf("templates/marimo/app.py.%s.tmpl", source)
	case FrameworkR:
		return "view.R", fmt.Sprintf("templates/r/view.R.%s.tmpl", source)
	case FrameworkSQL:
		return "view.sql", fmt.Sprintf("templates/sql/view.sql.%s.tmpl", source)
	default:
		return "view.txt", ""
	}
}

func needsPyproject(fw Framework) bool {
	return fw == FrameworkQuartoPython || fw == FrameworkJupyter || fw == FrameworkMarimo
}

func writeTemplate(dir, filename, tmplPath string, data scaffoldData, perm os.FileMode) error {
	tmplData, err := templateFS.ReadFile(tmplPath)
	if err != nil {
		return fmt.Errorf("read template %s: %w", tmplPath, err)
	}

	tmpl, err := template.New(filename).Parse(string(tmplData))
	if err != nil {
		return fmt.Errorf("parse template %s: %w", tmplPath, err)
	}

	path := filepath.Join(dir, filename)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, perm)
	if err != nil {
		return fmt.Errorf("create %s: %w", filename, err)
	}
	defer func() { _ = f.Close() }()

	if err := tmpl.Execute(f, data); err != nil {
		return fmt.Errorf("execute template %s: %w", tmplPath, err)
	}
	return nil
}
