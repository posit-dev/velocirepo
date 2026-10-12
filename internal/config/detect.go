package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

type Detected struct {
	ProjectID     string
	IDSource      string
	GitHub        string
	GitHubSource  string
	PyPI          string
	PyPISource    string
	CRAN          string
	CRANSource    string
	OpenVSX       string
	OpenVSXSource string
}

func DetectGitHub() (owner, repo string) {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return "", ""
	}
	url := strings.TrimSpace(string(out))
	return parseGitHubURL(url)
}

func parseGitHubURL(url string) (owner, repo string) {
	// SSH: git@github.com:owner/repo.git
	sshRe := regexp.MustCompile(`git@github\.com:([^/]+)/([^/]+?)(?:\.git)?$`)
	if m := sshRe.FindStringSubmatch(url); m != nil {
		return m[1], m[2]
	}
	// HTTPS: https://github.com/owner/repo.git
	httpsRe := regexp.MustCompile(`https://github\.com/([^/]+)/([^/]+?)(?:\.git)?$`)
	if m := httpsRe.FindStringSubmatch(url); m != nil {
		return m[1], m[2]
	}
	return "", ""
}

func DetectProjectID() string {
	_, repo := DetectGitHub()
	if repo == "" {
		return ""
	}
	id := strings.ToLower(repo)
	id = strings.ReplaceAll(id, ".", "-")
	return id
}

func DetectPyPI(dir string) string {
	path := filepath.Join(dir, "pyproject.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	var pyproject struct {
		Project struct {
			Name string `toml:"name"`
		} `toml:"project"`
	}
	if err := toml.Unmarshal(data, &pyproject); err != nil {
		return ""
	}
	return pyproject.Project.Name
}

func DetectCRAN(dir string) string {
	path := filepath.Join(dir, "DESCRIPTION")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Package:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Package:"))
		}
	}
	return ""
}

func DetectOpenVSX(dir string) string {
	path := filepath.Join(dir, "package.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	var pkg struct {
		Name      string `json:"name"`
		Publisher string `json:"publisher"`
		Engines   struct {
			VSCode string `json:"vscode"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return ""
	}

	if pkg.Engines.VSCode == "" {
		return ""
	}
	if pkg.Publisher == "" || pkg.Name == "" {
		return ""
	}
	return pkg.Publisher + "/" + pkg.Name
}

func DetectAll(dir string) Detected {
	d := Detected{}

	owner, repo := DetectGitHub()
	if owner != "" && repo != "" {
		d.GitHub = owner + "/" + repo
		d.GitHubSource = "git remote"
		id := strings.ToLower(repo)
		id = strings.ReplaceAll(id, ".", "-")
		d.ProjectID = id
		d.IDSource = "git remote"
	}

	if pypi := DetectPyPI(dir); pypi != "" {
		d.PyPI = pypi
		d.PyPISource = "pyproject.toml"
	}

	if cran := DetectCRAN(dir); cran != "" {
		d.CRAN = cran
		d.CRANSource = "DESCRIPTION"
	}

	if openvsx := DetectOpenVSX(dir); openvsx != "" {
		d.OpenVSX = openvsx
		d.OpenVSXSource = "package.json"
	}

	return d
}
