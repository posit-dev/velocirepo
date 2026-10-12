package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteLog(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	entries := []LogEntry{
		{
			RunID:      "2025-06-15T14:30:00Z",
			Timestamp:  "2025-06-15T14:30:00Z",
			Version:    "1.0.0",
			Command:    "fetch",
			Source:     "pypi",
			ProjectID:  "my-proj",
			Records:    42,
			Files:      2,
			StartDate:  "2025-06-01",
			EndDate:    "2025-06-14",
			DurationMS: 1234,
			StatusCode: 0,
		},
		{
			RunID:     "2025-06-15T14:30:00Z",
			Timestamp: "2025-06-15T14:30:00Z",
			Version:   "1.0.0",
			Command:   "fetch",
			Source:    "cran",
			ProjectID: "my-proj",
			Skipped:   "already up to date",
		},
	}

	if err := WriteLog(dataDir, entries); err != nil {
		t.Fatal(err)
	}

	path := LogFilePath(dataDir, "2025-06-15")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("log file not created: %v", err)
	}

	read, err := readJSONL[LogEntry](path, readJSONLOptions{wrapErrors: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(read) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(read))
	}
	if read[0].Source != "pypi" {
		t.Errorf("expected source=pypi, got %s", read[0].Source)
	}
	if read[0].DurationMS != 1234 {
		t.Errorf("expected duration_ms=1234, got %d", read[0].DurationMS)
	}
	if read[1].Skipped != "already up to date" {
		t.Errorf("expected skipped reason, got %s", read[1].Skipped)
	}
}

func TestWriteLogWithStatusCode(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	entries := []LogEntry{
		{
			RunID:      "2025-06-15T14:30:00Z",
			Timestamp:  "2025-06-15T14:30:00Z",
			Version:    "1.0.0",
			Command:    "fetch-pypi",
			Source:     "pypi",
			ProjectID:  "my-proj",
			StatusCode: 429,
			Error:      "PyPI API error 429: rate limited",
		},
	}

	if err := WriteLog(dataDir, entries); err != nil {
		t.Fatal(err)
	}

	path := LogFilePath(dataDir, "2025-06-15")
	read, err := readJSONL[LogEntry](path, readJSONLOptions{wrapErrors: true})
	if err != nil {
		t.Fatal(err)
	}
	if read[0].StatusCode != 429 {
		t.Errorf("expected status_code=429, got %d", read[0].StatusCode)
	}
}

func TestWriteLogAppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	first := []LogEntry{{
		RunID: "2025-06-15T10:00:00Z", Timestamp: "2025-06-15T10:00:00Z",
		Version: "1.0.0", Command: "fetch-pypi",
		Source: "pypi", ProjectID: "proj-a", Records: 10,
	}}
	if err := WriteLog(dataDir, first); err != nil {
		t.Fatal(err)
	}

	second := []LogEntry{{
		RunID: "2025-06-15T14:00:00Z", Timestamp: "2025-06-15T14:00:00Z",
		Version: "1.0.0", Command: "fetch",
		Source: "cran", ProjectID: "proj-a", Records: 5,
	}}
	if err := WriteLog(dataDir, second); err != nil {
		t.Fatal(err)
	}

	path := LogFilePath(dataDir, "2025-06-15")
	read, err := readJSONL[LogEntry](path, readJSONLOptions{wrapErrors: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(read) != 2 {
		t.Fatalf("expected 2 entries after append, got %d", len(read))
	}
	if read[0].Command != "fetch-pypi" {
		t.Errorf("expected first entry command=fetch-pypi, got %s", read[0].Command)
	}
	if read[1].Command != "fetch" {
		t.Errorf("expected second entry command=fetch, got %s", read[1].Command)
	}
}

func TestWriteLogCreatesSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	entries := []LogEntry{{
		RunID: "2025-06-15T10:00:00Z", Timestamp: "2025-06-15T10:00:00Z",
		Version: "1.0.0", Command: "fetch",
		Source: "pypi", ProjectID: "proj-a", Skipped: "no new records",
	}}
	if err := WriteLog(dataDir, entries); err != nil {
		t.Fatal(err)
	}

	if err := CheckSchemaVersion(dataDir); err != nil {
		t.Fatalf("WriteLog should create schema version marker: %v", err)
	}
}

func TestWriteLogEmpty(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	if err := WriteLog(dataDir, nil); err != nil {
		t.Fatal(err)
	}

	logDir := filepath.Join(dataDir, LogDir)
	if _, err := os.Stat(logDir); !os.IsNotExist(err) {
		t.Error("log directory should not be created for empty entries")
	}
}
