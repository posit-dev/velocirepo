package store

import (
	"bufio"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/posit-dev/velocirepo/internal/source"
)

// An interrupted write leaves its temp file behind. Leftovers from older
// versions (".tmp-*.jsonl") and from this one must never be read as data.
func TestQueriesIgnoreLeftoverTempFiles(t *testing.T) {
	dir := t.TempDir()
	record := source.Record{Metric: "daily_downloads", ProjectID: "p", Target: "pkg", Date: "2025-06-01", Value: 1500}
	if _, err := WriteRecords(dir, "pypi", "p", []source.Record{record}); err != nil {
		t.Fatal(err)
	}
	event := source.Event{Type: "star", ProjectID: "p", Target: "org/repo", Datetime: "2025-06-01T10:00:00Z", User: "alice"}
	if _, err := WriteEvents(dir, "github", "p", []source.Event{event}); err != nil {
		t.Fatal(err)
	}
	record.Source = "pypi"
	event.Source = "github"
	for _, name := range []string{".tmp-123.jsonl", ".2025-06-01.jsonl.456.tmp"} {
		writeTestRecords(t, filepath.Join(MetricsProjectDir(dir, "pypi", "p"), name), record)
		writeTestEvents(t, filepath.Join(EventsProjectDir(dir, "github", "p"), name), event)
	}

	rows, _, err := QueryLive(dir, nil, nil, "SELECT (SELECT SUM(value) FROM metrics WHERE metric = 'daily_downloads') AS downloads, (SELECT COUNT(*) FROM events) AS stars")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(rows[0]["downloads"]); got != "1500" {
		t.Errorf("downloads = %s, want 1500", got)
	}
	if got := fmt.Sprint(rows[0]["stars"]); got != "1" {
		t.Errorf("stars = %s, want 1", got)
	}
}

func TestQueriesTreatOnlyLeftoverTempFilesAsNoData(t *testing.T) {
	dir := t.TempDir()
	writeTestRecords(t, filepath.Join(MetricsProjectDir(dir, "pypi", "p"), ".tmp-123.jsonl"),
		source.Record{Source: "pypi", Metric: "daily_downloads", ProjectID: "p", Date: "2025-06-01", Value: 5})

	rows, _, err := QueryLive(dir, nil, nil, "SELECT COUNT(*) AS n FROM metrics")
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := rows[0]["n"].(int64); !ok || n != 0 {
		if b, ok := rows[0]["n"].(*big.Int); !ok || b.Sign() != 0 {
			t.Errorf("metrics rows = %v, want 0", rows[0]["n"])
		}
	}
}

func TestAtomicWriteTempFileIsHidden(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2025-06-01.jsonl")
	var tempNames []string
	err := writeJSONLWith(path, func(w *bufio.Writer) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			tempNames = append(tempNames, e.Name())
		}
		_, err = w.WriteString("{}\n")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tempNames) != 1 {
		t.Fatalf("files during the write = %v, want one temp file", tempNames)
	}
	if name := tempNames[0]; !strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".jsonl") {
		t.Errorf("temp file %q must be hidden and must not end in .jsonl", name)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("target not written: %v", err)
	}
}
