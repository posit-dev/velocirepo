package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/posit-dev/velocirepo/internal/version"
)

type InstallMethod int

const (
	MethodDirect    InstallMethod = iota
	MethodHomebrew
	MethodUV
	MethodPip
	MethodGoInstall
)

type Updater struct {
	Client     *http.Client
	APIBaseURL string
	DLBaseURL  string
	ExePath    string
	Version    string
}

type githubRelease struct {
	TagName string `json:"tag_name"`
}

func (u *Updater) Run(ctx context.Context, w io.Writer) error {
	currentVersion := u.Version
	if currentVersion == "" {
		currentVersion = version.Version
	}
	if currentVersion == "dev" {
		return fmt.Errorf("cannot update a dev build; install a release build first")
	}

	latest, err := u.checkLatest(ctx)
	if err != nil {
		return fmt.Errorf("check for updates: %w", err)
	}

	latestClean := strings.TrimPrefix(latest, "v")
	newer, err := isNewer(latestClean, currentVersion)
	if err != nil {
		return err
	}
	if !newer {
		fmt.Fprintf(w, "velocirepo %s is already up to date\n", currentVersion)
		return nil
	}

	method := u.detectInstallMethod()

	switch method {
	case MethodHomebrew:
		fmt.Fprintf(w, "A new version of velocirepo is available: %s\n", latestClean)
		fmt.Fprintf(w, "Installed via Homebrew. To upgrade, run:\n")
		fmt.Fprintf(w, "  brew upgrade velocirepo\n")
		return nil
	case MethodUV:
		fmt.Fprintf(w, "A new version of velocirepo is available: %s\n", latestClean)
		fmt.Fprintf(w, "Installed via uv. To upgrade, run:\n")
		fmt.Fprintf(w, "  uv tool upgrade velocirepo\n")
		return nil
	case MethodPip:
		fmt.Fprintf(w, "A new version of velocirepo is available: %s\n", latestClean)
		fmt.Fprintf(w, "Installed via pip. To upgrade, run:\n")
		fmt.Fprintf(w, "  pip install --upgrade velocirepo\n")
		return nil
	case MethodGoInstall:
		fmt.Fprintf(w, "A new version of velocirepo is available: %s\n", latestClean)
		fmt.Fprintf(w, "Installed via go install. To upgrade, run:\n")
		fmt.Fprintf(w, "  go install github.com/posit-dev/velocirepo/cmd/velocirepo@latest\n")
		return nil
	}

	return u.selfUpdate(ctx, w, latestClean)
}

func (u *Updater) checkLatest(ctx context.Context) (string, error) {
	apiBase := u.APIBaseURL
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}

	client := u.Client
	if client == nil {
		client = http.DefaultClient
	}

	url := apiBase + "/repos/posit-dev/velocirepo/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if release.TagName == "" {
		return "", fmt.Errorf("empty tag_name in response")
	}
	return release.TagName, nil
}

func (u *Updater) selfUpdate(ctx context.Context, w io.Writer, newVersion string) error {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	if goos != "linux" && goos != "darwin" {
		return fmt.Errorf("unsupported platform %s/%s; download from https://github.com/posit-dev/velocirepo/releases", goos, goarch)
	}
	if goarch != "amd64" && goarch != "arm64" {
		return fmt.Errorf("unsupported platform %s/%s; download from https://github.com/posit-dev/velocirepo/releases", goos, goarch)
	}

	resolved, err := u.resolvedExePath()
	if err != nil {
		return err
	}

	dlBase := u.DLBaseURL
	if dlBase == "" {
		dlBase = "https://github.com"
	}

	client := u.Client
	if client == nil {
		client = http.DefaultClient
	}

	artifact := fmt.Sprintf("velocirepo_%s_%s_%s.tar.gz", newVersion, goos, goarch)
	tgzURL := fmt.Sprintf("%s/posit-dev/velocirepo/releases/download/v%s/%s", dlBase, newVersion, artifact)
	sumsURL := fmt.Sprintf("%s/posit-dev/velocirepo/releases/download/v%s/SHA256SUMS", dlBase, newVersion)

	tmpDir, err := os.MkdirTemp(filepath.Dir(resolved), ".velocirepo-update-*")
	if err != nil {
		return fmt.Errorf("create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	fmt.Fprintf(w, "Updating velocirepo to %s...\n", newVersion)

	fmt.Fprintf(w, "  Downloading %s...\n", artifact)
	tgzPath := filepath.Join(tmpDir, artifact)
	if err := downloadFile(ctx, client, tgzURL, tgzPath); err != nil {
		return fmt.Errorf("download %s: %w", artifact, err)
	}

	fmt.Fprintf(w, "  Verifying checksum...\n")
	expectedHash, err := fetchExpectedHash(ctx, client, sumsURL, artifact)
	if err != nil {
		return err
	}
	if err := verifyChecksum(tgzPath, expectedHash); err != nil {
		return err
	}

	fmt.Fprintf(w, "  Installing...\n")
	newBinPath := filepath.Join(tmpDir, "velocirepo.new")
	if err := extractBinary(tgzPath, newBinPath); err != nil {
		return err
	}

	stat, err := os.Stat(resolved)
	if err == nil {
		_ = os.Chmod(newBinPath, stat.Mode())
	}

	if err := os.Rename(newBinPath, resolved); err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("cannot replace %s: %w\nTry: sudo velocirepo update", resolved, err)
		}
		return fmt.Errorf("replace binary: %w", err)
	}

	fmt.Fprintf(w, "Successfully updated velocirepo to %s\n", newVersion)
	return nil
}

func (u *Updater) resolvedExePath() (string, error) {
	exe := u.ExePath
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return "", fmt.Errorf("resolve executable: %w", err)
		}
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return exe, nil
	}
	return resolved, nil
}

func (u *Updater) detectInstallMethod() InstallMethod {
	resolved, err := u.resolvedExePath()
	if err != nil {
		return MethodDirect
	}

	if strings.Contains(resolved, "/Cellar/velocirepo/") {
		return MethodHomebrew
	}

	home, _ := os.UserHomeDir()
	if home != "" {
		uvTools := filepath.Join(home, ".local", "share", "uv", "tools", "velocirepo")
		if strings.HasPrefix(resolved, uvTools+string(filepath.Separator)) {
			return MethodUV
		}
	}

	binDir := filepath.Dir(resolved)
	envDir := filepath.Dir(binDir)
	if _, statErr := os.Stat(filepath.Join(envDir, "pyvenv.cfg")); statErr == nil {
		return MethodPip
	}

	if home != "" {
		gopath := os.Getenv("GOPATH")
		if gopath == "" {
			gopath = filepath.Join(home, "go")
		}
		if resolved == filepath.Join(gopath, "bin", "velocirepo") {
			return MethodGoInstall
		}
	}

	return MethodDirect
}

func parseVersion(s string) ([3]int, error) {
	s = strings.TrimPrefix(s, "v")
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 {
		return [3]int{}, fmt.Errorf("invalid version %q", s)
	}
	var v [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return [3]int{}, fmt.Errorf("invalid version %q: %w", s, err)
		}
		v[i] = n
	}
	return v, nil
}

func isNewer(latest, current string) (bool, error) {
	lv, err := parseVersion(latest)
	if err != nil {
		return false, fmt.Errorf("parse latest version: %w", err)
	}
	cv, err := parseVersion(current)
	if err != nil {
		return false, fmt.Errorf("parse current version: %w", err)
	}
	for i := range lv {
		switch {
		case lv[i] > cv[i]:
			return true, nil
		case lv[i] < cv[i]:
			return false, nil
		}
	}
	return false, nil
}

func downloadFile(ctx context.Context, client *http.Client, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

func fetchExpectedHash(ctx context.Context, client *http.Client, sumsURL, artifact string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sumsURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download SHA256SUMS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download SHA256SUMS: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read SHA256SUMS: %w", err)
	}

	for _, line := range strings.Split(string(body), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == artifact {
			return parts[0], nil
		}
	}
	return "", fmt.Errorf("no checksum found for %s in SHA256SUMS", artifact)
}

func verifyChecksum(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("verify checksum: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("verify checksum: %w", err)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if got != expected {
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, expected)
	}
	return nil
}

func extractBinary(tgzPath, dest string) error {
	f, err := os.Open(tgzPath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("decompress archive: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if filepath.Base(hdr.Name) == "velocirepo" && hdr.Typeflag == tar.TypeReg {
			out, err := os.Create(dest)
			if err != nil {
				return err
			}
			defer out.Close()
			if _, err := io.Copy(out, tr); err != nil {
				return fmt.Errorf("extract binary: %w", err)
			}
			return nil
		}
	}
	return fmt.Errorf("binary 'velocirepo' not found in archive")
}
