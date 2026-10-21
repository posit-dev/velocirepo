package store

import (
	"fmt"
	"os"
	"path/filepath"
)

type LogEntry struct {
	RunID      string `json:"run_id"`
	Timestamp  string `json:"timestamp"`
	Version    string `json:"version"`
	Command    string `json:"command"`
	Source     string `json:"source"`
	ProjectID  string `json:"project_id"`
	Records    int    `json:"records"`
	Files      int    `json:"files,omitempty"`
	StartDate  string `json:"start_date,omitempty"`
	EndDate    string `json:"end_date,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	Skipped    string `json:"skipped,omitempty"`
	Error      string `json:"error,omitempty"`
}

func WriteLog(dataDir string, entries []LogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	date := entries[0].RunID[:10]
	dir := filepath.Join(dataDir, LogDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create log directory %s: %w", dir, err)
	}

	path := LogFilePath(dataDir, date)

	existing, _ := readJSONL[LogEntry](path, readJSONLOptions{skipInvalid: true})
	all := append(existing, entries...)

	if err := writeJSONLAtomic(path, all, "log entry"); err != nil {
		return err
	}

	ensureSchemaVersion(dataDir)
	return nil
}

// ReadLog reads the fetch log of a day (YYYY-MM-DD).
func ReadLog(dataDir, date string) ([]LogEntry, error) {
	return readJSONL[LogEntry](LogFilePath(dataDir, date), readJSONLOptions{wrapErrors: true})
}
