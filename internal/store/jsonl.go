package store

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/posit-dev/velocirepo/internal/dateutil"
	"github.com/posit-dev/velocirepo/internal/source"
)

var (
	dailyPattern   = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.jsonl$`)
	monthlyPattern = regexp.MustCompile(`^(\d{4}-\d{2})\.jsonl$`)
	yearlyPattern  = regexp.MustCompile(`^(\d{4})\.jsonl$`)
)

// keepUnreplaced returns the stored records whose series isn't in records.
func keepUnreplaced(stored, records []source.Record) []source.Record {
	replaced := make(map[string]bool, len(records))
	for _, r := range records {
		replaced[dedupKey(r)] = true
	}
	var kept []source.Record
	for _, r := range stored {
		if !replaced[dedupKey(r)] {
			kept = append(kept, r)
		}
	}
	return kept
}

func writeFileAtomic(path string, records []source.Record) error {
	return writeJSONLAtomic(path, records, "record")
}

func ReadRecords(path string) ([]source.Record, error) {
	return readJSONL[source.Record](path, readJSONLOptions{wrapErrors: true})
}

// LastDate returns the latest date stored for a source and project, which the
// next fetch resumes after. A monthly or yearly file counts as stored through
// the last day of its period; compaction only creates one once that day is
// stored (see aggregateProject).
func LastDate(dataDir, sourceName, projectID string) (time.Time, error) {
	latest, err := latestStoredDate(SourceProjectDir(dataDir, sourceName, projectID), WatermarkFilePath(dataDir, sourceName, projectID))
	if err != nil || latest == "" {
		return time.Time{}, err
	}

	t, err := dateutil.ParseDate(latest)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse date %s: %w", latest, err)
	}
	return t, nil
}

// StoredThrough returns the latest day stored for a project's source in a
// category ("metrics" or "events"), from its files and main watermark, or "".
func StoredThrough(dataDir, category, sourceName, projectID string) (string, error) {
	return latestStoredDate(filepath.Join(dataDir, category, sourceName, projectID), WatermarkFilePath(dataDir, sourceName, projectID))
}

// latestStoredDate returns the latest date among dir's data file names and the
// watermark file, or "" if there is none.
func latestStoredDate(dir, watermarkPath string) (string, error) {
	dates, err := dateStringsInDir(dir)
	if err != nil {
		return "", err
	}

	watermarks, err := readMetricWatermarks(watermarkPath)
	if err != nil {
		return "", err
	}
	for _, w := range watermarks {
		if w.Date != "" {
			dates = append(dates, w.Date)
		}
	}

	if len(dates) == 0 {
		return "", nil
	}
	sort.Strings(dates)
	return dates[len(dates)-1], nil
}

func dateStringsInDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read directory %s: %w", dir, err)
	}

	var dates []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()

		if m := dailyPattern.FindStringSubmatch(name); m != nil {
			dates = append(dates, m[1])
		} else if m := monthlyPattern.FindStringSubmatch(name); m != nil {
			dates = append(dates, dateutil.LastDayOfMonth(m[1]))
		} else if m := yearlyPattern.FindStringSubmatch(name); m != nil {
			dates = append(dates, m[1]+"-12-31")
		}
	}

	return dates, nil
}

func groupByDate(records []source.Record) map[string][]source.Record {
	return groupBy(records, func(r source.Record) string { return r.Date })
}

func WriteEvents(dataDir, sourceName, projectID string, events []source.Event) (int, error) {
	for i := range events {
		events[i].Source = sourceName
	}

	grouped := groupEventsByDate(events)

	var filesWritten int
	for date, dateEvents := range grouped {
		dir := EventsProjectDir(dataDir, sourceName, projectID)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return filesWritten, fmt.Errorf("create directory %s: %w", dir, err)
		}

		path := filepath.Join(dir, date+".jsonl")
		if err := writeEventsFileAtomic(path, dateEvents); err != nil {
			return filesWritten, err
		}
		filesWritten++
	}

	ensureSchemaVersion(dataDir)
	return filesWritten, nil
}

func writeEventsFileAtomic(path string, events []source.Event) error {
	return writeJSONLAtomic(path, events, "event")
}

func ReadEvents(path string) ([]source.Event, error) {
	return readJSONL[source.Event](path, readJSONLOptions{wrapErrors: true})
}

func groupEventsByDate(events []source.Event) map[string][]source.Event {
	return groupBy(events, func(e source.Event) string {
		date := e.Datetime[:10]
		return date
	})
}

type DirStats struct {
	LastDate string
	Records  int
	Size     int64
}

func ScanProjectDir(dir string) DirStats {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return DirStats{}
	}

	var stats DirStats
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		recs, _ := ReadRecords(path)
		stats.Records += len(recs)

		if info, err := e.Info(); err == nil {
			stats.Size += info.Size()
		}

		datePart := strings.TrimSuffix(e.Name(), ".jsonl")
		if datePart > stats.LastDate {
			stats.LastDate = datePart
		}
	}
	return stats
}
