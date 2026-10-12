package selfupdate

// Tests for install.sh.
//
// The script's download base URL is overridable via VELOCIREPO_BASE_URL so
// that these tests can point it at a local httptest server instead of GitHub.
// The tests require curl, tar and sha256sum / shasum to be on PATH, which is
// guaranteed on both macOS and Linux.  They are skipped automatically when
// those tools are absent.

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installShPath returns the absolute path to install.sh at the repository root.
func installShPath(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	// This file is at internal/selfupdate/install_script_test.go.
	// The root is two directories up.
	root := filepath.Join(filepath.Dir(filename), "..", "..")
	p := filepath.Join(root, "install.sh")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("install.sh not found at %s: %v", p, err)
	}
	return p
}

// requireTool skips the test if name is not on PATH.
func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not found on PATH: %v", name, err)
	}
}

// makeFakeRelease creates a minimal tar.gz containing a fake velocirepo
// binary (a shell script) and returns the archive bytes and its hex-encoded
// SHA-256 digest.
func makeFakeRelease(t *testing.T) ([]byte, string) {
	t.Helper()

	// Build the tar.gz in memory.
	var buf strings.Builder
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	content := []byte("#!/bin/sh\necho fake-velocirepo-v1.2.3\n")
	hdr := &tar.Header{
		Name:     "velocirepo",
		Typeflag: tar.TypeReg,
		Mode:     0755,
		Size:     int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatal(err)
	}

	data := []byte(buf.String())
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:])
}

// startFakeReleaseServer returns a test server that serves a fake release for
// version 1.2.3 on linux/amd64 and darwin/amd64 (and arm64).  artifact is the
// tar.gz content, sha256hex is its expected checksum, and wrongSHA causes the
// SHA256SUMS file to carry an incorrect hash instead.
func startFakeReleaseServer(t *testing.T, artifact []byte, sha256hex string, wrongSHA bool) *httptest.Server {
	t.Helper()

	version := "1.2.3"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redirect /releases/latest to the version tag.
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			http.Redirect(w, r, "/releases/tag/v"+version, http.StatusFound)
			return
		}
		// Handle the version redirect (curl follows it and reads the final URL).
		if strings.HasSuffix(r.URL.Path, "/releases/tag/v"+version) {
			http.Redirect(w, r, "/releases/tag/v"+version, http.StatusFound)
			return
		}

		base := "/posit-dev/velocirepo/releases/download/v" + version + "/"
		name := strings.TrimPrefix(r.URL.Path, base)

		if name == "SHA256SUMS" {
			checksum := sha256hex
			if wrongSHA {
				checksum = strings.Repeat("0", 64)
			}
			// SHA256SUMS has two-space separator, one line per artifact.
			for _, os := range []string{"linux", "darwin"} {
				for _, arch := range []string{"amd64", "arm64"} {
					fmt.Fprintf(w, "%s  velocirepo_%s_%s_%s.tar.gz\n", checksum, version, os, arch)
				}
			}
			return
		}

		// Serve any matching .tar.gz.
		if strings.HasSuffix(name, ".tar.gz") {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(artifact)
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runInstallSh executes install.sh with the given env overrides and captures
// its combined output.  It returns the exit error (nil on success).
func runInstallSh(t *testing.T, installDir, baseURL string, extraEnv ...string) (string, error) {
	t.Helper()

	script := installShPath(t)
	cmd := exec.Command("sh", script)
	cmd.Env = append(os.Environ(),
		"INSTALL_DIR="+installDir,
		"VERSION=1.2.3",
		"VELOCIREPO_BASE_URL="+baseURL,
	)
	cmd.Env = append(cmd.Env, extraEnv...)

	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestInstallShSyntax(t *testing.T) {
	// sh -n checks POSIX syntax without executing any commands.
	requireTool(t, "sh")
	script := installShPath(t)
	out, err := exec.Command("sh", "-n", script).CombinedOutput()
	if err != nil {
		t.Fatalf("sh -n install.sh failed:\n%s", out)
	}
}

func TestInstallShVerifiesCorrectChecksum(t *testing.T) {
	requireTool(t, "sh")
	requireTool(t, "curl")

	artifact, sha256hex := makeFakeRelease(t)
	srv := startFakeReleaseServer(t, artifact, sha256hex, false)

	installDir := t.TempDir()
	out, err := runInstallSh(t, installDir, srv.URL)
	if err != nil {
		t.Fatalf("install.sh failed (expected success):\n%s", err)
	}

	binary := filepath.Join(installDir, "velocirepo")
	if _, statErr := os.Stat(binary); statErr != nil {
		t.Fatalf("binary not found at %s after install (output: %s)", binary, out)
	}
}

func TestInstallShRejectsWrongChecksum(t *testing.T) {
	requireTool(t, "sh")
	requireTool(t, "curl")

	artifact, sha256hex := makeFakeRelease(t)
	srv := startFakeReleaseServer(t, artifact, sha256hex, true /* wrongSHA */)

	installDir := t.TempDir()
	out, err := runInstallSh(t, installDir, srv.URL)
	if err == nil {
		t.Fatalf("install.sh succeeded with wrong checksum; output:\n%s", out)
	}
	if !strings.Contains(out, "mismatch") {
		t.Errorf("expected 'mismatch' in output, got:\n%s", out)
	}

	// The binary must NOT have been installed.
	binary := filepath.Join(installDir, "velocirepo")
	if _, statErr := os.Stat(binary); statErr == nil {
		t.Errorf("binary was installed despite checksum mismatch")
	}
}

func TestInstallShRejectsMissingChecksum(t *testing.T) {
	requireTool(t, "sh")
	requireTool(t, "curl")

	artifact, _ := makeFakeRelease(t)

	// Serve a SHA256SUMS that contains no entry for the requested artifact.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/SHA256SUMS") {
			// File exists but has no entry for this artifact.
			fmt.Fprintf(w, "%s  some_other_file.tar.gz\n", strings.Repeat("a", 64))
			return
		}
		if strings.HasSuffix(r.URL.Path, ".tar.gz") {
			_, _ = w.Write(artifact)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	installDir := t.TempDir()
	out, err := runInstallSh(t, installDir, srv.URL)
	if err == nil {
		t.Fatalf("install.sh succeeded with missing checksum entry; output:\n%s", out)
	}
	if !strings.Contains(out, "no checksum found") {
		t.Errorf("expected 'no checksum found' in output, got:\n%s", out)
	}
}
