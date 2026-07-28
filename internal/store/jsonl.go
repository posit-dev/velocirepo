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

func WriteRecords(dataDir, sourceName, projectID string, records []source.Record) (int, error) {
	for i := range records {
		records[i].Source = sourceName
	}

	dir := MetricsProjectDir(dataDir, sourceName, projectID)

	wantedTotalKeys := totalKeys(records)
	var lastValues map[string]int64
	if len(wantedTotalKeys) > 0 {
		var err error
		lastValues, err = lastRecordedTotalsFor(dir, wantedTotalKeys)
		if err != nil {
			return 0, fmt.Errorf("read last recorded totals: %w", err)
		}
	}

	grouped := groupByDate(records)
	dates := make([]string, 0, len(grouped))
	for d := range grouped {
		dates = append(dates, d)
	}
	sort.Strings(dates)

	var filesWritten int
	for _, date := range dates {
		dateRecords := filterUnchangedTotals(grouped[date], lastValues)

		if len(dateRecords) > 0 {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return filesWritten, fmt.Errorf("create directory %s: %w", dir, err)
			}

			path := filepath.Join(dir, date+".jsonl")
			if err := writeFileAtomic(path, dateRecords); err != nil {
				return filesWritten, err
			}
			filesWritten++

			for _, r := range dateRecords {
				if isTotalMetric(r.Metric) {
					if lastValues == nil {
						lastValues = make(map[string]int64)
					}
					lastValues[totalKey(r)] = r.Value
				}
			}
		}

		if err := writeMetricWatermarks(dataDir, sourceName, projectID, date, grouped[date]); err != nil {
			return filesWritten, err
		}
	}

	ensureSchemaVersion(dataDir)
	return filesWritten, nil
}

func writeFileAtomic(path string, records []source.Record) error {
	return writeJSONLAtomic(path, records, "record")
}

func ReadRecords(path string) ([]source.Record, error) {
	return readJSONL[source.Record](path, readJSONLOptions{wrapErrors: true})
}

func LastDate(dataDir, sourceName, projectID string) (time.Time, error) {
	dates, err := dateStringsInDir(SourceProjectDir(dataDir, sourceName, projectID))
	if err != nil {
		return time.Time{}, err
	}

	watermarks, err := readMetricWatermarks(WatermarkFilePath(dataDir, sourceName, projectID))
	if err != nil {
		return time.Time{}, err
	}
	for _, w := range watermarks {
		if w.Date != "" {
			dates = append(dates, w.Date)
		}
	}

	if len(dates) == 0 {
		return time.Time{}, nil
	}

	sort.Strings(dates)
	latest := dates[len(dates)-1]

	t, err := dateutil.ParseDate(latest)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse date %s: %w", latest, err)
	}
	return t, nil
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
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
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
