package fetch

import (
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

func TestResolveEndDate(t *testing.T) {
	now := time.Date(2025, 3, 1, 1, 30, 0, 0, time.UTC)
	tests := []struct {
		name       string
		configured string
		flag       string
		want       string
	}{
		{"default is yesterday", "", "", "2025-02-28"},
		{"configured yesterday", "yesterday", "", "2025-02-28"},
		{"configured date", "2025-01-31", "", "2025-01-31"},
		{"flag wins", "2025-01-31", "2025-02-15", "2025-02-15"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Settings: config.SettingsConfig{EndDate: tt.configured}}
			got, err := resolveEndDate(cfg, tt.flag, now)
			if err != nil {
				t.Fatal(err)
			}
			if got.Format("2006-01-02") != tt.want || !got.Equal(got.Truncate(24*time.Hour)) {
				t.Errorf("end date = %s, want %s at midnight UTC", got.Format(time.RFC3339), tt.want)
			}
		})
	}
}

func TestResolveStartDate(t *testing.T) {
	now := time.Date(2025, 3, 20, 1, 0, 0, 0, time.UTC)
	write := func(t *testing.T, dataDir, sourceName, date string) {
		t.Helper()
		if _, err := store.WriteRecords(dataDir, sourceName, "proj", []source.Record{
			{Metric: "daily_views", ProjectID: "proj", Target: "org/repo", Date: date, Value: 1},
		}); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name       string
		sourceName string
		lastStored string
		flag       string
		want       string
	}{
		{"nothing stored", "pypi", "", "", "2000-01-01"},
		{"resumes the day after the last stored date", "pypi", "2025-03-10", "", "2025-03-11"},
		{"flag wins", "pypi", "2025-03-10", "2025-01-01", "2025-01-01"},
		{"traffic within its window", "github-traffic", "2025-03-15", "", "2025-03-16"},
		{"traffic clamped to its 14-day window", "github-traffic", "2025-02-01", "", "2025-03-06"},
		{"traffic first fetch", "github-traffic", "", "", "2025-03-06"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dataDir := t.TempDir()
			if tt.lastStored != "" {
				write(t, dataDir, tt.sourceName, tt.lastStored)
			}
			got, err := resolveStartDate(dataDir, tt.sourceName, "proj", tt.flag, now)
			if err != nil {
				t.Fatal(err)
			}
			if got.Format("2006-01-02") != tt.want {
				t.Errorf("start date = %s, want %s", got.Format("2006-01-02"), tt.want)
			}
		})
	}
}
