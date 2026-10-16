package config

import (
	"os"
	"strings"
	"testing"

	"github.com/posit-dev/velocirepo/internal/testutil"
)

func TestFindSection(t *testing.T) {
	lines := []string{
		"[data]",
		`dir = "data"`,
		"",
		"[projects.alpha]",
		`name = "Alpha"`,
		`github = "org/alpha"`,
		"",
		"[projects.beta]",
		`name = "Beta"`,
	}

	start, end, found := FindSection(lines, "projects.alpha")
	if !found {
		t.Fatal("expected to find section")
	}
	if start != 3 {
		t.Errorf("start = %d, want 3", start)
	}
	if end != 7 {
		t.Errorf("end = %d, want 7", end)
	}

	start, end, found = FindSection(lines, "projects.beta")
	if !found {
		t.Fatal("expected to find section")
	}
	if start != 7 {
		t.Errorf("start = %d, want 7", start)
	}
	if end != 9 {
		t.Errorf("end = %d, want 9 (len)", end)
	}

	_, _, found = FindSection(lines, "projects.missing")
	if found {
		t.Error("expected not found for missing section")
	}
}

func TestAppendProject(t *testing.T) {
	dir := t.TempDir()
	initial := "[data]\ndir = \"data\"\n\n[projects.alpha]\nname = \"Alpha\"\n"
	path := testutil.WriteTempFile(t, dir, "velocirepo.toml", initial)

	err := AppendProject(path, "beta", Project{
		Name:         "Beta",
		GitHubEvents: StringList{"org/beta"},
		PyPI:         StringList{"beta-pkg"},
	})
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	if !strings.Contains(content, "[projects.beta]") {
		t.Error("missing [projects.beta] section")
	}
	if !strings.Contains(content, `github = "org/beta"`) {
		t.Error("missing github field")
	}
	if !strings.Contains(content, `pypi = "beta-pkg"`) {
		t.Error("missing pypi field")
	}
	// Original content preserved
	if !strings.Contains(content, "[projects.alpha]") {
		t.Error("original section lost")
	}
}

func TestAppendProjectPreservesComments(t *testing.T) {
	dir := t.TempDir()
	initial := "# Main config\n[data]\ndir = \"data\"\n\n# Alpha project\n[projects.alpha]\nname = \"Alpha\"\n"
	path := testutil.WriteTempFile(t, dir, "velocirepo.toml", initial)

	err := AppendProject(path, "beta", Project{Name: "Beta", GitHubEvents: StringList{"org/beta"}})
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	if !strings.Contains(content, "# Main config") {
		t.Error("lost top comment")
	}
	if !strings.Contains(content, "# Alpha project") {
		t.Error("lost section comment")
	}
}

func TestRemoveProjectMiddle(t *testing.T) {
	dir := t.TempDir()
	initial := `[data]
dir = "data"

[projects.alpha]
name = "Alpha"

[projects.beta]
name = "Beta"

[projects.gamma]
name = "Gamma"
`
	path := testutil.WriteTempFile(t, dir, "velocirepo.toml", initial)

	err := RemoveProject(path, "beta")
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	if strings.Contains(content, "beta") {
		t.Error("beta section still present")
	}
	if !strings.Contains(content, "[projects.alpha]") {
		t.Error("alpha section lost")
	}
	if !strings.Contains(content, "[projects.gamma]") {
		t.Error("gamma section lost")
	}
}

func TestRemoveProjectEnd(t *testing.T) {
	dir := t.TempDir()
	initial := `[data]
dir = "data"

[projects.alpha]
name = "Alpha"

[projects.beta]
name = "Beta"
`
	path := testutil.WriteTempFile(t, dir, "velocirepo.toml", initial)

	err := RemoveProject(path, "beta")
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	if strings.Contains(content, "beta") {
		t.Error("beta section still present")
	}
	if !strings.Contains(content, "[projects.alpha]") {
		t.Error("alpha section lost")
	}
}

func TestRemoveProjectNotFound(t *testing.T) {
	dir := t.TempDir()
	path := testutil.WriteTempFile(t, dir, "velocirepo.toml", "[data]\ndir = \"data\"\n")

	err := RemoveProject(path, "nope")
	if err == nil {
		t.Fatal("expected error for missing project")
	}
}

func TestUpdateProjectModifyField(t *testing.T) {
	dir := t.TempDir()
	initial := `[projects.alpha]
name = "Alpha"
github = "org/alpha"
pypi = "alpha"
`
	path := testutil.WriteTempFile(t, dir, "velocirepo.toml", initial)

	err := UpdateProject(path, "alpha", map[string]string{"name": "Alpha v2"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	if !strings.Contains(content, `name = "Alpha v2"`) {
		t.Errorf("name not updated, got:\n%s", content)
	}
	if !strings.Contains(content, `github = "org/alpha"`) {
		t.Error("github field lost")
	}
}

func TestUpdateProjectAddField(t *testing.T) {
	dir := t.TempDir()
	initial := `[projects.alpha]
name = "Alpha"
github = "org/alpha"
`
	path := testutil.WriteTempFile(t, dir, "velocirepo.toml", initial)

	err := UpdateProject(path, "alpha", map[string]string{"pypi": "alpha-pkg"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	if !strings.Contains(content, `pypi = "alpha-pkg"`) {
		t.Errorf("pypi not added, got:\n%s", content)
	}
}

func TestUpdateProjectUnset(t *testing.T) {
	dir := t.TempDir()
	initial := `[projects.alpha]
name = "Alpha"
github = "org/alpha"
pypi = "alpha"
`
	path := testutil.WriteTempFile(t, dir, "velocirepo.toml", initial)

	err := UpdateProject(path, "alpha", nil, []string{"pypi"})
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	if strings.Contains(content, "pypi") {
		t.Errorf("pypi still present, got:\n%s", content)
	}
	if !strings.Contains(content, `github = "org/alpha"`) {
		t.Error("github field lost")
	}
}

func TestUpdateProjectNotFound(t *testing.T) {
	dir := t.TempDir()
	path := testutil.WriteTempFile(t, dir, "velocirepo.toml", "[data]\ndir = \"data\"\n")

	err := UpdateProject(path, "nope", map[string]string{"name": "x"}, nil)
	if err == nil {
		t.Fatal("expected error for missing project")
	}
}

func TestRenameSection(t *testing.T) {
	dir := t.TempDir()
	initial := `[projects.old-name]
name = "Old"
github = "org/old"
`
	path := testutil.WriteTempFile(t, dir, "velocirepo.toml", initial)

	err := RenameSection(path, "old-name", "new-name")
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	if strings.Contains(content, "old-name") {
		t.Error("old header still present")
	}
	if !strings.Contains(content, "[projects.new-name]") {
		t.Error("new header not found")
	}
	if !strings.Contains(content, `github = "org/old"`) {
		t.Error("body content lost")
	}
}

func TestRenameSectionNotFound(t *testing.T) {
	dir := t.TempDir()
	path := testutil.WriteTempFile(t, dir, "velocirepo.toml", "[data]\ndir = \"data\"\n")

	err := RenameSection(path, "nope", "new")
	if err == nil {
		t.Fatal("expected error for missing section")
	}
}

func TestWriteFileAtomicallyContentCorrect(t *testing.T) {
	dir := t.TempDir()
	path := testutil.WriteTempFile(t, dir, "test.toml", "original content\n")

	if err := writeFileAtomically(path, []byte("new content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new content\n" {
		t.Errorf("got %q, want %q", string(data), "new content\n")
	}
}

func TestWriteFileAtomicallyNoTempLeftover(t *testing.T) {
	dir := t.TempDir()
	path := testutil.WriteTempFile(t, dir, "test.toml", "original\n")

	if err := writeFileAtomically(path, []byte("updated\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// No .tmp files should remain in the directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestWriteFileAtomicallyPreservesMode(t *testing.T) {
	dir := t.TempDir()
	path := testutil.WriteTempFile(t, dir, "test.toml", "original\n")

	// Set an uncommon mode on the original file.
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}

	if err := writeFileAtomically(path, []byte("updated\n"), 0); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Mode should be preserved (0640).
	if info.Mode().Perm() != 0640 {
		t.Errorf("mode = %o, want 0640", info.Mode().Perm())
	}
}

func TestAppendProjectIsAtomic(t *testing.T) {
	dir := t.TempDir()
	initial := "[data]\ndir = \"data\"\n\n[projects.alpha]\nname = \"Alpha\"\n"
	path := testutil.WriteTempFile(t, dir, "velocirepo.toml", initial)

	if err := AppendProject(path, "beta", Project{Name: "Beta", GitHubEvents: StringList{"org/beta"}}); err != nil {
		t.Fatal(err)
	}

	// No temp files in the directory after the call.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left after AppendProject: %s", e.Name())
		}
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "[projects.beta]") {
		t.Error("beta section not written")
	}
}
