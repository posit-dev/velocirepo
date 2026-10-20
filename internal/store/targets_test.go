package store

import (
	"reflect"
	"testing"

	"github.com/posit-dev/velocirepo/internal/source"
)

func TestTargetsWithMetric(t *testing.T) {
	dataDir := t.TempDir()
	records := []source.Record{
		{Metric: "daily_stars", ProjectID: "proj", Target: "org/a", Date: "2025-06-01", Value: 2},
		{Metric: "total_stars", ProjectID: "proj", Target: "org/b", Date: "2025-06-01", Value: 9},
	}
	if _, err := WriteRecords(dataDir, "github", "proj", records); err != nil {
		t.Fatal(err)
	}

	got, err := TargetsWithMetric(dataDir, "github", "proj", "daily_stars")
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]bool{"org/a": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("TargetsWithMetric = %v, want %v", got, want)
	}

	none, err := TargetsWithMetric(dataDir, "github", "other", "daily_stars")
	if err != nil || len(none) != 0 {
		t.Errorf("TargetsWithMetric for a project without data = %v, %v; want none", none, err)
	}
}
