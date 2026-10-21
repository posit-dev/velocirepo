package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/posit-dev/velocirepo/internal/source"
)

// writeTarget returns the file a day's data belongs in: its daily file if it
// exists, otherwise the monthly or yearly file that already holds its
// period, otherwise a new daily file. So data for a period that's already
// concatenated, from a backfill, a repair or a new target, goes into the
// period file, and velocirepo never creates daily files next to one.
func writeTarget(dir, date string) string {
	daily := filepath.Join(dir, date+".jsonl")
	if _, err := os.Stat(daily); err == nil {
		return daily
	}
	if len(date) >= 7 {
		if monthly := filepath.Join(dir, date[:7]+".jsonl"); fileExists(monthly) {
			return monthly
		}
	}
	if len(date) >= 4 {
		if yearly := filepath.Join(dir, date[:4]+".jsonl"); fileExists(yearly) {
			return yearly
		}
	}
	return daily
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// pendingFiles holds the files a write changes, so each is read and written
// once, also when a backfill covers many days of a concatenated year.
type pendingFiles[T any] struct {
	read    func(string) ([]T, error)
	write   func(string, []T) error
	less    func(a, b T) bool
	items   map[string][]T
	changed map[string]bool
}

func newPending[T any](read func(string) ([]T, error), write func(string, []T) error, less func(a, b T) bool) *pendingFiles[T] {
	return &pendingFiles[T]{read: read, write: write, less: less, items: map[string][]T{}, changed: map[string]bool{}}
}

func (p *pendingFiles[T]) load(path string) ([]T, error) {
	if items, ok := p.items[path]; ok {
		return items, nil
	}
	items, err := p.read(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	p.items[path] = items
	return items, nil
}

func (p *pendingFiles[T]) set(path string, items []T) {
	p.items[path] = items
	p.changed[path] = true
}

// flush writes the changed files, sorted, and removes the ones left empty.
func (p *pendingFiles[T]) flush(dir string) (int, error) {
	paths := make([]string, 0, len(p.changed))
	for path := range p.changed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	written := 0
	for _, path := range paths {
		items := p.items[path]
		if len(items) == 0 {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return written, fmt.Errorf("remove %s: %w", path, err)
			}
			continue
		}
		sort.SliceStable(items, func(i, j int) bool { return p.less(items[i], items[j]) })
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return written, fmt.Errorf("create directory %s: %w", dir, err)
		}
		if err := p.write(path, items); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

// dataFilesOverlapping returns the data files in dir whose period overlaps
// any of the windows.
func dataFilesOverlapping(dir string, windows map[string]source.Window) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read directory %s: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		first, last, _, ok := periodSpan(e.Name())
		if !ok {
			continue
		}
		for _, w := range windows {
			if windowOverlaps(w, first, last) {
				out = append(out, filepath.Join(dir, e.Name()))
				break
			}
		}
	}
	return out, nil
}

func windowOverlaps(w source.Window, first, last string) bool {
	if !w.Start.IsZero() && last < w.Start.Format("2006-01-02") {
		return false
	}
	return first <= w.End.Format("2006-01-02")
}

// WriteRecords writes records, merging them into the stored data: a record
// replaces the stored record of the same series and day, and other records
// stay. total_* series only store changes: a total equal to the latest value
// before its day isn't stored, but still replaces a value stored for that
// day.
func WriteRecords(dataDir, sourceName, projectID string, records []source.Record) (int, error) {
	for i := range records {
		records[i].Source = sourceName
	}
	dir := MetricsProjectDir(dataDir, sourceName, projectID)
	wantedTotalKeys := totalKeys(records)

	grouped := groupByDate(records)
	dates := make([]string, 0, len(grouped))
	for d := range grouped {
		dates = append(dates, d)
	}
	sort.Strings(dates)

	files := newPending(ReadRecords, writeFileAtomic, recordLess)
	// Totals written earlier in this batch aren't on disk yet.
	pendingTotals := map[string]totalState{}
	for _, date := range dates {
		var lastValues map[string]int64
		if len(wantedTotalKeys) > 0 {
			states, err := lastRecordedTotalStates(dir, wantedTotalKeys, date)
			if err != nil {
				return 0, fmt.Errorf("read last recorded totals: %w", err)
			}
			lastValues = make(map[string]int64, len(states)+len(pendingTotals))
			for key, st := range states {
				lastValues[key] = st.value
			}
			for key, st := range pendingTotals {
				if prev, ok := states[key]; !ok || st.date >= prev.date {
					lastValues[key] = st.value
				}
			}
		}
		dateRecords := filterUnchangedTotals(grouped[date], lastValues)
		for _, r := range dateRecords {
			if isTotalMetric(r.Metric) {
				pendingTotals[totalKey(r)] = totalState{value: r.Value, date: r.Date}
			}
		}

		path := writeTarget(dir, date)
		existing, err := files.load(path)
		if err != nil {
			return 0, err
		}
		kept := keepUnreplaced(existing, grouped[date])
		if len(dateRecords) > 0 || len(kept) < len(existing) {
			files.set(path, append(kept, dateRecords...))
		}
		if err := writeMetricWatermarks(dataDir, sourceName, projectID, date, grouped[date]); err != nil {
			return 0, err
		}
	}

	n, err := files.flush(dir)
	if err != nil {
		return n, err
	}
	ensureSchemaVersion(dataDir)
	return n, nil
}

func recordLess(a, b source.Record) bool { return a.Date < b.Date }

func eventLess(a, b source.Event) bool { return a.Datetime < b.Datetime }

// RecordScope is what a scoped write replaces: for each target, the days in
// its window, and of those days the given metrics (all metrics if none).
type RecordScope struct {
	Windows map[string]source.Window
	Metrics []string
}

func (s RecordScope) covers(r source.Record) bool {
	w, ok := s.Windows[r.Target]
	if !ok || !windowCovers(w, r.Date) {
		return false
	}
	if len(s.Metrics) == 0 {
		return true
	}
	for _, m := range s.Metrics {
		if m == r.Metric {
			return true
		}
	}
	return false
}

// WriteRecordsScoped replaces everything in scope with records, in daily and
// period files alike. A day in scope without a record ends up without one,
// so a repair can correct a day to "no data", which a merge can't express.
// Records are written as given: no total_* change-point filtering.
func WriteRecordsScoped(dataDir, sourceName, projectID string, records []source.Record, scope RecordScope) (int, error) {
	for i := range records {
		records[i].Source = sourceName
	}
	dir := MetricsProjectDir(dataDir, sourceName, projectID)
	files := newPending(ReadRecords, writeFileAtomic, recordLess)

	touched, err := dataFilesOverlapping(dir, scope.Windows)
	if err != nil {
		return 0, err
	}
	newByFile := map[string][]source.Record{}
	for _, r := range records {
		path := writeTarget(dir, r.Date)
		newByFile[path] = append(newByFile[path], r)
	}
	for path := range newByFile {
		touched = append(touched, path)
	}

	done := map[string]bool{}
	for _, path := range touched {
		if done[path] {
			continue
		}
		done[path] = true
		existing, err := files.load(path)
		if err != nil {
			return 0, err
		}
		var kept []source.Record
		for _, r := range existing {
			if !scope.covers(r) {
				kept = append(kept, r)
			}
		}
		files.set(path, append(kept, newByFile[path]...))
	}

	n, err := files.flush(dir)
	if err != nil {
		return n, err
	}
	ensureSchemaVersion(dataDir)
	return n, nil
}

// WriteEventsScoped writes events, replacing for each target only the days in
// its window, in daily and period files alike: events of other targets, and
// of days outside the window, stay as they are. A target's window says what's
// true for those days, so an event that's no longer there, such as a deleted
// comment, is removed, and a corrected event replaces the old one.
//
// WriteEvents replaces whole days, which is only right when every target of a
// project was fetched over the same days.
func WriteEventsScoped(dataDir, sourceName, projectID string, events []source.Event, windows map[string]source.Window) (int, error) {
	for i := range events {
		events[i].Source = sourceName
	}
	dir := EventsProjectDir(dataDir, sourceName, projectID)
	files := newPending(ReadEvents, writeEventsFileAtomic, eventLess)

	touched, err := dataFilesOverlapping(dir, windows)
	if err != nil {
		return 0, err
	}
	newByFile := map[string][]source.Event{}
	// A target with new events but no window replaces its events of those
	// days, as WriteEvents would.
	unwindowed := map[string]map[string]bool{}
	for _, e := range events {
		date := e.Datetime[:10]
		path := writeTarget(dir, date)
		newByFile[path] = append(newByFile[path], e)
		if _, ok := windows[e.Target]; !ok {
			if unwindowed[e.Target] == nil {
				unwindowed[e.Target] = map[string]bool{}
			}
			unwindowed[e.Target][date] = true
		}
	}
	for path := range newByFile {
		touched = append(touched, path)
	}

	done := map[string]bool{}
	for _, path := range touched {
		if done[path] {
			continue
		}
		done[path] = true
		existing, err := files.load(path)
		if err != nil {
			return 0, err
		}
		var kept []source.Event
		for _, e := range existing {
			date := e.Datetime[:10]
			if w, ok := windows[e.Target]; ok && windowCovers(w, date) {
				continue
			}
			if unwindowed[e.Target][date] {
				continue
			}
			kept = append(kept, e)
		}
		files.set(path, append(kept, newByFile[path]...))
	}

	n, err := files.flush(dir)
	if err != nil {
		return n, err
	}
	ensureSchemaVersion(dataDir)
	return n, nil
}
