package store

import (
	"os"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/source"
)

func TestAggregateDailyToMonthly(t *testing.T) {
	dir := t.TempDir()

	// Create 3 daily files for January 2025
	for day := 1; day <= 3; day++ {
		date := time.Date(2025, 1, day, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		records := []source.Record{
			{Metric: "downloads", ProjectID: "mylib", Date: date, Value: int64(day * 100)},
		}
		if _, err := WriteRecords(dir, "pypi", "mylib", records); err != nil {
			t.Fatal(err)
		}
	}

	// Run aggregate with "now" being Feb 2025 (January is complete)
	now := time.Date(2025, 2, 5, 0, 0, 0, 0, time.UTC)
	if err := Aggregate(dir, now); err != nil {
		t.Fatalf("Aggregate failed: %v", err)
	}

	// Monthly file should exist
	monthlyPath := metricsPath(dir, "pypi", "mylib", "2025-01")
	if _, err := os.Stat(monthlyPath); err != nil {
		t.Fatalf("monthly file not created: %s", monthlyPath)
	}

	// Daily files should be deleted
	for day := 1; day <= 3; day++ {
		date := time.Date(2025, 1, day, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		dailyPath := metricsPath(dir, "pypi", "mylib", date)
		if _, err := os.Stat(dailyPath); !os.IsNotExist(err) {
			t.Errorf("daily file not deleted: %s", dailyPath)
		}
	}

	// Read monthly file and verify contents
	records, err := ReadRecords(monthlyPath)
	if err != nil {
		t.Fatalf("ReadRecords failed: %v", err)
	}
	if len(records) != 3 {
		t.Errorf("got %d records, want 3", len(records))
	}
}

func TestAggregateSkipsIncompleteMonth(t *testing.T) {
	dir := t.TempDir()

	// Create daily files for June 2025
	records := []source.Record{
		{Metric: "downloads", ProjectID: "mylib", Date: "2025-06-01", Value: 100},
	}
	if _, err := WriteRecords(dir, "pypi", "mylib", records); err != nil {
		t.Fatal(err)
	}

	// Run aggregate with "now" being June 15 (month not complete)
	now := time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC)
	if err := Aggregate(dir, now); err != nil {
		t.Fatalf("Aggregate failed: %v", err)
	}

	// Monthly file should NOT exist
	monthlyPath := metricsPath(dir, "pypi", "mylib", "2025-06")
	if _, err := os.Stat(monthlyPath); !os.IsNotExist(err) {
		t.Error("monthly file should not be created for incomplete month")
	}

	// Daily file should still exist
	dailyPath := metricsPath(dir, "pypi", "mylib", "2025-06-01")
	if _, err := os.Stat(dailyPath); err != nil {
		t.Errorf("daily file should still exist: %s", dailyPath)
	}
}

func TestAggregateMonthlyToYearly(t *testing.T) {
	dir := t.TempDir()

	// Create monthly files for 2024
	for month := 1; month <= 3; month++ {
		date := time.Date(2024, time.Month(month), 15, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		records := []source.Record{
			{Metric: "downloads", ProjectID: "pkg", Date: date, Value: int64(month * 1000)},
		}
		monthStr := time.Date(2024, time.Month(month), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		writeTestRecords(t, metricsPath(dir, "cran", "pkg", monthStr), records...)
	}

	// Run aggregate with "now" being 2025 (2024 is complete)
	now := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := Aggregate(dir, now); err != nil {
		t.Fatalf("Aggregate failed: %v", err)
	}

	// Yearly file should exist
	yearlyPath := metricsPath(dir, "cran", "pkg", "2024")
	if _, err := os.Stat(yearlyPath); err != nil {
		t.Fatalf("yearly file not created: %s", yearlyPath)
	}

	// Monthly files should be deleted
	for month := 1; month <= 3; month++ {
		monthStr := time.Date(2024, time.Month(month), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		monthlyPath := metricsPath(dir, "cran", "pkg", monthStr)
		if _, err := os.Stat(monthlyPath); !os.IsNotExist(err) {
			t.Errorf("monthly file not deleted: %s", monthlyPath)
		}
	}

	records, err := ReadRecords(yearlyPath)
	if err != nil {
		t.Fatalf("ReadRecords failed: %v", err)
	}
	if len(records) != 3 {
		t.Errorf("got %d records, want 3", len(records))
	}
}

func TestAggregateDedup(t *testing.T) {
	dir := t.TempDir()

	// Write duplicate records across two daily files
	r1 := []source.Record{
		{Metric: "downloads", ProjectID: "mylib", Date: "2025-01-01", Value: 100},
	}
	r2 := []source.Record{
		{Metric: "downloads", ProjectID: "mylib", Date: "2025-01-01", Value: 100},
		{Metric: "downloads", ProjectID: "mylib", Date: "2025-01-02", Value: 200},
	}

	writeTestRecords(t, metricsPath(dir, "pypi", "mylib", "2025-01-01"), r1...)
	writeTestRecords(t, metricsPath(dir, "pypi", "mylib", "2025-01-02"), r2...)

	now := time.Date(2025, 2, 5, 0, 0, 0, 0, time.UTC)
	if err := Aggregate(dir, now); err != nil {
		t.Fatalf("Aggregate failed: %v", err)
	}

	monthlyPath := metricsPath(dir, "pypi", "mylib", "2025-01")
	records, err := ReadRecords(monthlyPath)
	if err != nil {
		t.Fatalf("ReadRecords failed: %v", err)
	}

	// Should have 2 unique records, not 3
	if len(records) != 2 {
		t.Errorf("got %d records after dedup, want 2", len(records))
	}
}

func TestAggregateDedupWithTags(t *testing.T) {
	dir := t.TempDir()

	records := []source.Record{
		{Metric: "views", ProjectID: "proj", Date: "2025-01-01", Value: 100, Extra: map[string]string{"video_id": "a"}},
		{Metric: "views", ProjectID: "proj", Date: "2025-01-01", Value: 200, Extra: map[string]string{"video_id": "b"}},
		{Metric: "views", ProjectID: "proj", Date: "2025-01-01", Value: 100, Extra: map[string]string{"video_id": "a"}},
	}

	writeTestRecords(t, metricsPath(dir, "youtube", "proj", "2025-01-01"), records...)

	now := time.Date(2025, 2, 5, 0, 0, 0, 0, time.UTC)
	if err := Aggregate(dir, now); err != nil {
		t.Fatalf("Aggregate failed: %v", err)
	}

	monthlyPath := metricsPath(dir, "youtube", "proj", "2025-01")
	got, err := ReadRecords(monthlyPath)
	if err != nil {
		t.Fatalf("ReadRecords failed: %v", err)
	}

	// video_id=a and video_id=b are distinct, duplicate video_id=a is removed
	if len(got) != 2 {
		t.Errorf("got %d records after dedup, want 2", len(got))
	}
}
