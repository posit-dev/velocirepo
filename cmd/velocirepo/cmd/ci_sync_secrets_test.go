package cmd

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/posit-dev/velocirepo/internal/auth"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

func TestParseEnvFile(t *testing.T) {
	dir := t.TempDir()
	// Use double quotes (the format auth.UpsertEnvFile writes) so that the
	// unescaping path is exercised. Single-quoted values are intentionally
	// not unquoted: auth.quoteEnvValue only writes double quotes.
	envPath := writeEnvFile(t, dir, `# comment
PLAUSIBLE_API_KEY=abc123
GITHUB_TOKEN="ghp_secret"
YOUTUBE_TOKEN="yt_key"

EMPTY=
`)

	secrets, err := parseEnvFile(envPath)
	if err != nil {
		t.Fatal(err)
	}

	if secrets["PLAUSIBLE_API_KEY"] != "abc123" {
		t.Errorf("PLAUSIBLE_API_KEY = %q, want abc123", secrets["PLAUSIBLE_API_KEY"])
	}
	if secrets["GITHUB_TOKEN"] != "ghp_secret" {
		t.Errorf("GITHUB_TOKEN = %q, want ghp_secret", secrets["GITHUB_TOKEN"])
	}
	if secrets["YOUTUBE_TOKEN"] != "yt_key" {
		t.Errorf("YOUTUBE_TOKEN = %q, want yt_key", secrets["YOUTUBE_TOKEN"])
	}
	if secrets["EMPTY"] != "" {
		t.Errorf("EMPTY = %q, want empty", secrets["EMPTY"])
	}
}

func TestParseEnvFileRoundTripsSpecialValues(t *testing.T) {
	// Verify that values written by auth.UpsertEnvFile (which uses
	// quoteEnvValue) are recovered identically by parseEnvFile (which now
	// uses auth.UnquoteEnvValue). This is the end-to-end sync-secrets path.
	values := map[string]string{
		"DOLLAR":    "token$foo",
		"QUOTE":     `has"quote`,
		"BACKTICK":  "back`tick",
		"BANG":      "bang!val",
		"BACKSLASH": `back\slash`,
		"SPACE":     "has space",
		"HASH":      "has#comment",
		"EQUALS":    "has=sign",
		"SINGLE":    "has'single",
		"NEWLINE":   "line1\nline2",
		"CRLF":      "line1\r\nline2",
	}

	dir := t.TempDir()
	envPath := writeTempFile(t, dir, ".env", "")

	// Write values through auth.UpsertEnvFile, then read through parseEnvFile.
	if err := auth.UpsertEnvFile(envPath, values); err != nil {
		t.Fatal(err)
	}

	got, err := parseEnvFile(envPath)
	if err != nil {
		t.Fatal(err)
	}

	for key, want := range values {
		if got[key] != want {
			t.Errorf("key %s: got %q, want %q", key, got[key], want)
		}
	}
}

func TestEncryptSecret(t *testing.T) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	encrypted, err := encryptSecret(pub[:], "hello")
	if err != nil {
		t.Fatal(err)
	}

	ciphertext, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		t.Fatal(err)
	}

	decrypted, ok := box.OpenAnonymous(nil, ciphertext, pub, priv)
	if !ok {
		t.Fatal("failed to decrypt")
	}
	if string(decrypted) != "hello" {
		t.Errorf("decrypted = %q, want hello", string(decrypted))
	}
}

func TestCiSyncSecretsDryRun(t *testing.T) {
	cfgPath, envPath := setupConfigAndEnv(t, `[data]
dir = "data"
`, "MY_SECRET=value123\n")

	_, buf, err := execCmd(cfgPath, "sync-secrets", "--dry-run", "--env-file", envPath, "--repo", "owner/repo")
	if err != nil {
		t.Fatal(err)
	}

	output := buf.String()
	assertContains(t, output, "MY_SECRET")
	assertContains(t, output, "Would sync 1 secret")
}

func TestCiSyncSecretsDryRunRenamesGitHub(t *testing.T) {
	cfgPath, envPath := setupConfigAndEnv(t, `[data]
dir = "data"
`, "GITHUB_TOKEN=ghp_secret\nPLAUSIBLE_TOKEN=abc\n")

	_, buf, err := execCmd(cfgPath, "sync-secrets", "--dry-run", "--env-file", envPath, "--repo", "owner/repo")
	if err != nil {
		t.Fatal(err)
	}

	output := buf.String()
	assertContains(t, output, "GH_TOKEN")
	assertNotContains(t, output, "GITHUB_TOKEN")
	assertContains(t, output, "Would sync 2 secret")
}

func TestCiSyncSecretsIntegration(t *testing.T) {
	var privateKey [32]byte
	rand.Read(privateKey[:])
	var publicKey [32]byte
	curve25519.ScalarBaseMult(&publicKey, &privateKey)

	secretsSet := make(map[string]string)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/actions/secrets/public-key" {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"key":    base64.StdEncoding.EncodeToString(publicKey[:]),
				"key_id": "key-123",
			})
			return
		}

		if r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/repos/owner/repo/actions/secrets/") {
			name := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/actions/secrets/")
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			secretsSet[name] = body["encrypted_value"]
			w.WriteHeader(http.StatusCreated)
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	oldBase := githubAPIBase
	githubAPIBase = server.URL
	defer func() { githubAPIBase = oldBase }()

	cfgPath, envPath := setupConfigAndEnv(t, `[data]
dir = "data"
`, "PLAUSIBLE_API_KEY=test123\nYOUTUBE_TOKEN=yt456\n")

	t.Setenv("GITHUB_TOKEN", "fake-token")

	_, buf, err := execCmd(cfgPath, "sync-secrets", "--force", "--env-file", envPath, "--repo", "owner/repo")
	if err != nil {
		t.Fatal(err)
	}

	output := buf.String()
	assertContains(t, output, "Synced 2 secret")

	if _, ok := secretsSet["PLAUSIBLE_API_KEY"]; !ok {
		t.Error("PLAUSIBLE_API_KEY not set")
	}
	if _, ok := secretsSet["YOUTUBE_TOKEN"]; !ok {
		t.Error("YOUTUBE_TOKEN not set")
	}

	// Verify we can decrypt
	for name, enc := range secretsSet {
		ciphertext, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		_, ok := box.OpenAnonymous(nil, ciphertext, &publicKey, &privateKey)
		if !ok {
			t.Errorf("failed to decrypt %s", name)
		}
	}
}
