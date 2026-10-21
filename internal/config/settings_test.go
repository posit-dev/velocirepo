package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadConfigText(t *testing.T, text string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "velocirepo.toml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestGateAndArchiveSettings(t *testing.T) {
	cfg, err := loadConfigText(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Settings.Enforce() {
		t.Error("the gate enforces without [settings] gate = \"enforce\"")
	}
	if !cfg.Settings.ArchiveEnabled() {
		t.Error("the archive is off by default")
	}

	cfg, err = loadConfigText(t, "[settings]\ngate = \"enforce\"\narchive = false\n")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Settings.Enforce() || cfg.Settings.ArchiveEnabled() {
		t.Errorf("settings = %+v, want enforcing without archive", cfg.Settings)
	}

	if _, err := loadConfigText(t, "[settings]\ngate = \"strict\"\n"); err == nil || !strings.Contains(err.Error(), "gate") {
		t.Errorf("an invalid gate value loaded: %v", err)
	}
}
