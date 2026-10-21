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

func TestTimezoneSettings(t *testing.T) {
	cfg, err := loadConfigText(t, "[settings.timezones]\n\"plotnine.org\" = \"America/Los_Angeles\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Settings.Timezones["plotnine.org"] != "America/Los_Angeles" {
		t.Errorf("timezones = %v", cfg.Settings.Timezones)
	}
	if _, err := loadConfigText(t, "[settings.timezones]\n\"plotnine.org\" = \"Mars/Olympus\"\n"); err == nil || !strings.Contains(err.Error(), "plotnine.org") {
		t.Errorf("an invalid time zone loaded: %v", err)
	}
}

func TestConcatenationDay(t *testing.T) {
	cfg, err := loadConfigText(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Settings.ConcatenationDay(); got != DefaultConcatenateOnDay {
		t.Errorf("default concatenation day = %d", got)
	}
	cfg, err = loadConfigText(t, "[settings]\nconcatenate_on_day = 2\n")
	if err != nil || cfg.Settings.ConcatenationDay() != 2 {
		t.Errorf("concatenation day = %v, %v; want 2", cfg, err)
	}
	if _, err := loadConfigText(t, "[settings]\nconcatenate_on_day = 31\n"); err == nil || !strings.Contains(err.Error(), "concatenate_on_day") {
		t.Errorf("day 31 loaded: %v", err)
	}
}
