package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		input string
		want  [3]int
	}{
		{"1.2.3", [3]int{1, 2, 3}},
		{"v1.2.3", [3]int{1, 2, 3}},
		{"0.0.1", [3]int{0, 0, 1}},
		{"10.20.30", [3]int{10, 20, 30}},
	}
	for _, tt := range tests {
		got, err := parseVersion(tt.input)
		if err != nil {
			t.Errorf("parseVersion(%q) error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseVersion(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseVersion_Invalid(t *testing.T) {
	inputs := []string{"", "1.2", "1.2.3.4", "abc", "v1.x.3"}
	for _, input := range inputs {
		_, err := parseVersion(input)
		if err == nil {
			t.Errorf("parseVersion(%q) expected error", input)
		}
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		latest, current string
		want            bool
	}{
		{"1.2.3", "1.2.3", false},
		{"1.2.4", "1.2.3", true},
		{"1.3.0", "1.2.9", true},
		{"2.0.0", "1.9.9", true},
		{"1.2.3", "1.2.4", false},
		{"1.2.3", "2.0.0", false},
		{"v1.3.0", "v1.2.0", true},
	}
	for _, tt := range tests {
		got, err := isNewer(tt.latest, tt.current)
		if err != nil {
			t.Errorf("isNewer(%q, %q) error: %v", tt.latest, tt.current, err)
			continue
		}
		if got != tt.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
		}
	}
}

func TestRun_DevBuild(t *testing.T) {
	u := &Updater{Version: "dev"}
	var buf strings.Builder
	err := u.Run(context.Background(), &buf)
	if err == nil {
		t.Fatal("expected error for dev build")
	}
	if !strings.Contains(err.Error(), "dev build") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRun_AlreadyUpToDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name": "v1.2.3"}`)
	}))
	defer srv.Close()

	u := &Updater{
		Version:    "1.2.3",
		APIBaseURL: srv.URL,
		Client:     srv.Client(),
	}
	var buf strings.Builder
	err := u.Run(context.Background(), &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "already up to date") {
		t.Errorf("expected up-to-date message, got: %s", buf.String())
	}
}

func TestRun_Homebrew(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name": "v2.0.0"}`)
	}))
	defer srv.Close()

	u := &Updater{
		Version:    "1.0.0",
		APIBaseURL: srv.URL,
		Client:     srv.Client(),
		ExePath:    "/opt/homebrew/Cellar/velocirepo/1.0.0/bin/velocirepo",
	}
	var buf strings.Builder
	err := u.Run(context.Background(), &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "brew upgrade velocirepo") {
		t.Errorf("expected brew command, got: %s", buf.String())
	}
}

func TestRun_UVTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name": "v2.0.0"}`)
	}))
	defer srv.Close()

	home, _ := os.UserHomeDir()
	uvPath := filepath.Join(home, ".local", "share", "uv", "tools", "velocirepo", "bin", "velocirepo")

	u := &Updater{
		Version:    "1.0.0",
		APIBaseURL: srv.URL,
		Client:     srv.Client(),
		ExePath:    uvPath,
	}
	var buf strings.Builder
	err := u.Run(context.Background(), &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "uv tool upgrade velocirepo") {
		t.Errorf("expected uv command, got: %s", buf.String())
	}
}

func TestRun_Pip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name": "v2.0.0"}`)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	envDir := filepath.Join(tmp, "myenv")
	binDir := filepath.Join(envDir, "bin")
	os.MkdirAll(binDir, 0o755)
	os.WriteFile(filepath.Join(envDir, "pyvenv.cfg"), []byte("home = /usr/bin\n"), 0o644)
	exePath := filepath.Join(binDir, "velocirepo")
	os.WriteFile(exePath, []byte("binary"), 0o755)

	u := &Updater{
		Version:    "1.0.0",
		APIBaseURL: srv.URL,
		Client:     srv.Client(),
		ExePath:    exePath,
	}
	var buf strings.Builder
	err := u.Run(context.Background(), &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "pip install --upgrade velocirepo") {
		t.Errorf("expected pip command, got: %s", buf.String())
	}
}

func TestRun_GoInstall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name": "v2.0.0"}`)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	// Resolve symlinks so the path matches what EvalSymlinks returns (macOS /var → /private/var)
	tmp, _ = filepath.EvalSymlinks(tmp)
	binDir := filepath.Join(tmp, "bin")
	os.MkdirAll(binDir, 0o755)
	exePath := filepath.Join(binDir, "velocirepo")
	os.WriteFile(exePath, []byte("binary"), 0o755)

	t.Setenv("GOPATH", tmp)

	u := &Updater{
		Version:    "1.0.0",
		APIBaseURL: srv.URL,
		Client:     srv.Client(),
		ExePath:    exePath,
	}
	var buf strings.Builder
	err := u.Run(context.Background(), &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "go install github.com/posit-dev/velocirepo/cmd/velocirepo@latest") {
		t.Errorf("expected go install command, got: %s", buf.String())
	}
}

func TestRun_SelfUpdate_Success(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho updated\n")

	var tgzBuf bytes.Buffer
	gz := gzip.NewWriter(&tgzBuf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{
		Name: "velocirepo",
		Mode: 0o755,
		Size: int64(len(binaryContent)),
	})
	tw.Write(binaryContent)
	tw.Close()
	gz.Close()

	tgzBytes := tgzBuf.Bytes()
	h := sha256.Sum256(tgzBytes)
	checksum := hex.EncodeToString(h[:])
	artifact := fmt.Sprintf("velocirepo_2.0.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	sumsContent := fmt.Sprintf("%s  %s\n", checksum, artifact)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "releases/latest"):
			fmt.Fprintf(w, `{"tag_name": "v2.0.0"}`)
		case strings.HasSuffix(r.URL.Path, ".tar.gz"):
			w.Write(tgzBytes)
		case strings.HasSuffix(r.URL.Path, "SHA256SUMS"):
			fmt.Fprint(w, sumsContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmp := t.TempDir()
	exePath := filepath.Join(tmp, "velocirepo")
	os.WriteFile(exePath, []byte("old binary"), 0o755)

	u := &Updater{
		Version:    "1.0.0",
		APIBaseURL: srv.URL,
		DLBaseURL:  srv.URL,
		Client:     srv.Client(),
		ExePath:    exePath,
	}
	var buf strings.Builder
	err := u.Run(context.Background(), &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(exePath)
	if err != nil {
		t.Fatalf("read updated binary: %v", err)
	}
	if !bytes.Equal(got, binaryContent) {
		t.Errorf("binary content mismatch: got %q", got)
	}
	if !strings.Contains(buf.String(), "Successfully updated") {
		t.Errorf("expected success message, got: %s", buf.String())
	}
}

func TestRun_SelfUpdate_ChecksumMismatch(t *testing.T) {
	binaryContent := []byte("binary")

	var tgzBuf bytes.Buffer
	gz := gzip.NewWriter(&tgzBuf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{
		Name: "velocirepo",
		Mode: 0o755,
		Size: int64(len(binaryContent)),
	})
	tw.Write(binaryContent)
	tw.Close()
	gz.Close()

	tgzBytes := tgzBuf.Bytes()
	wrongChecksum := "0000000000000000000000000000000000000000000000000000000000000000"
	artifact := fmt.Sprintf("velocirepo_2.0.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	sumsContent := fmt.Sprintf("%s  %s\n", wrongChecksum, artifact)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "releases/latest"):
			fmt.Fprintf(w, `{"tag_name": "v2.0.0"}`)
		case strings.HasSuffix(r.URL.Path, ".tar.gz"):
			w.Write(tgzBytes)
		case strings.HasSuffix(r.URL.Path, "SHA256SUMS"):
			fmt.Fprint(w, sumsContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmp := t.TempDir()
	exePath := filepath.Join(tmp, "velocirepo")
	os.WriteFile(exePath, []byte("old binary"), 0o755)

	u := &Updater{
		Version:    "1.0.0",
		APIBaseURL: srv.URL,
		DLBaseURL:  srv.URL,
		Client:     srv.Client(),
		ExePath:    exePath,
	}
	var buf strings.Builder
	err := u.Run(context.Background(), &buf)
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("unexpected error: %v", err)
	}

	got, _ := os.ReadFile(exePath)
	if string(got) != "old binary" {
		t.Error("binary should not have been replaced on checksum failure")
	}
}

func TestDetectInstallMethod_Direct(t *testing.T) {
	tmp := t.TempDir()
	exePath := filepath.Join(tmp, "velocirepo")
	os.WriteFile(exePath, []byte("binary"), 0o755)

	u := &Updater{ExePath: exePath}
	if got := u.detectInstallMethod(); got != MethodDirect {
		t.Errorf("expected MethodDirect, got %d", got)
	}
}

func TestDetectInstallMethod_Homebrew(t *testing.T) {
	u := &Updater{ExePath: "/opt/homebrew/Cellar/velocirepo/1.0.0/bin/velocirepo"}
	if got := u.detectInstallMethod(); got != MethodHomebrew {
		t.Errorf("expected MethodHomebrew, got %d", got)
	}
}
