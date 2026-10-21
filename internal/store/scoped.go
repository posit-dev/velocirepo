package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/posit-dev/velocirepo/internal/dateutil"
	"github.com/posit-dev/velocirepo/internal/source"
)

// WriteEventsScoped writes events, replacing for each target only the days in
// its window: events of other targets, and of days outside the window, stay
// as they are. A target's window says what's true for those days, so an
// event that's no longer there, such as a deleted comment, is removed.
//
// WriteEvents replaces whole days, which is only right when every target of a
// project was fetched over the same days.
func WriteEventsScoped(dataDir, sourceName, projectID string, events []source.Event, windows map[string]source.Window) (int, error) {
	for i := range events {
		events[i].Source = sourceName
	}
	dir := EventsProjectDir(dataDir, sourceName, projectID)
	newByDate := groupEventsByDate(events)

	dates := make(map[string]bool, len(newByDate))
	for date := range newByDate {
		dates[date] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("read directory %s: %w", dir, err)
	}
	for _, e := range entries {
		m := dailyPattern.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		for _, w := range windows {
			if windowCovers(w, m[1]) {
				dates[m[1]] = true
				break
			}
		}
	}

	sorted := make([]string, 0, len(dates))
	for date := range dates {
		sorted = append(sorted, date)
	}
	sort.Strings(sorted)

	var filesWritten int
	for _, date := range sorted {
		path := filepath.Join(dir, date+".jsonl")
		existing, err := ReadEvents(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return filesWritten, fmt.Errorf("read %s: %w", path, err)
		}

		// A target with new events that day but no window replaces its
		// events of that day, as WriteEvents would.
		replaced := make(map[string]bool)
		for _, e := range newByDate[date] {
			if _, ok := windows[e.Target]; !ok {
				replaced[e.Target] = true
			}
		}

		var merged []source.Event
		for _, e := range existing {
			w, ok := windows[e.Target]
			if (ok && windowCovers(w, date)) || replaced[e.Target] {
				continue
			}
			merged = append(merged, e)
		}
		merged = append(merged, newByDate[date]...)

		if len(merged) == 0 {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return filesWritten, fmt.Errorf("remove %s: %w", path, err)
			}
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return filesWritten, fmt.Errorf("create directory %s: %w", dir, err)
		}
		if err := writeEventsFileAtomic(path, merged); err != nil {
			return filesWritten, err
		}
		filesWritten++
	}

	ensureSchemaVersion(dataDir)
	return filesWritten, nil
}

// windowCovers reports whether a window holds a day (YYYY-MM-DD).
func windowCovers(w source.Window, date string) bool {
	d, err := dateutil.ParseDate(date)
	if err != nil {
		return false
	}
	if !w.Start.IsZero() && d.Before(w.Start) {
		return false
	}
	return !d.After(w.End.Truncate(24 * time.Hour))
}
