package store

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/sourceinfo"
)

// SourceCategory returns the date-partitioned category ("metrics" or "events")
// for a given source. Sources may also produce content via the ContentProvider
// interface, which is handled separately by the fetch orchestrator.
func SourceCategory(sourceName string) string {
	return sourceinfo.CategoryDir(sourceName)
}

// Aggregate concatenates daily→monthly→yearly for all projects. Errors for
// individual projects are logged but not propagated; category directories that
// don't exist yet are silently skipped.
func Aggregate(dataDir string, now time.Time) error {
	aggregateCategory(filepath.Join(dataDir, MetricsDir), now, recordAggregateSpec())
	aggregateCategory(filepath.Join(dataDir, EventsDir), now, eventAggregateSpec())
	return nil
}

type aggregateSpec[T any] struct {
	read  func(string) ([]T, error)
	write func(string, []T) error
	dedup func([]T) []T
	less  func(T, T) bool
	label string
}

func recordAggregateSpec() aggregateSpec[source.Record] {
	return aggregateSpec[source.Record]{
		read:  ReadRecords,
		write: writeFileAtomic,
		dedup: dedup,
		less: func(a, b source.Record) bool {
			return a.Date < b.Date
		},
		label: "records",
	}
}

func eventAggregateSpec() aggregateSpec[source.Event] {
	return aggregateSpec[source.Event]{
		read:  ReadEvents,
		write: writeEventsFileAtomic,
		dedup: dedupEvents,
		less: func(a, b source.Event) bool {
			return a.Datetime < b.Datetime
		},
		label: "events",
	}
}

func aggregateCategory[T any](categoryDir string, now time.Time, spec aggregateSpec[T]) {
	sourceDirs, err := os.ReadDir(categoryDir)
	if err != nil {
		return
	}

	for _, sourceEntry := range sourceDirs {
		if !sourceEntry.IsDir() {
			continue
		}
		sourcePath := filepath.Join(categoryDir, sourceEntry.Name())

		projectDirs, err := os.ReadDir(sourcePath)
		if err != nil {
			continue
		}

		for _, projEntry := range projectDirs {
			if !projEntry.IsDir() {
				continue
			}
			projPath := filepath.Join(sourcePath, projEntry.Name())
			if err := aggregateProject(projPath, now, spec); err != nil {
				slog.Warn("concatenation failed", "path", projPath, "error", err)
			}
		}
	}
}

func aggregateProject[T any](projDir string, now time.Time, spec aggregateSpec[T]) error {
	if err := aggregateDailyToMonthly(projDir, now, spec); err != nil {
		return err
	}
	return aggregateMonthlyToYearly(projDir, now, spec)
}

func aggregateDailyToMonthly[T any](projDir string, now time.Time, spec aggregateSpec[T]) error {
	return aggregateFiles(projDir, now, spec, groupDailyFile, isMonthComplete, "month", "daily to monthly")
}

func aggregateMonthlyToYearly[T any](projDir string, now time.Time, spec aggregateSpec[T]) error {
	return aggregateFiles(projDir, now, spec, groupMonthlyFile, isYearComplete, "year", "monthly to yearly")
}

func aggregateFiles[T any](
	projDir string,
	now time.Time,
	spec aggregateSpec[T],
	groupFile func(string) (string, bool),
	isComplete func(string, time.Time) bool,
	logKey string,
	logLabel string,
) error {
	entries, err := os.ReadDir(projDir)
	if err != nil {
		return err
	}

	groupedFiles := make(map[string][]string)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		group, ok := groupFile(entry.Name())
		if ok {
			groupedFiles[group] = append(groupedFiles[group], filepath.Join(projDir, entry.Name()))
		}
	}

	for group, files := range groupedFiles {
		if !isComplete(group, now) {
			continue
		}

		sort.Strings(files)

		items, err := readAllAggregateFiles(files, spec.read)
		if err != nil {
			return fmt.Errorf("read %s files for %s: %w", spec.label, group, err)
		}

		items = spec.dedup(items)
		sort.Slice(items, func(i, j int) bool {
			return spec.less(items[i], items[j])
		})

		outPath := filepath.Join(projDir, group+".jsonl")
		if err := spec.write(outPath, items); err != nil {
			return err
		}

		removeAggregateSources(files)

		slog.Debug("concatenated "+logLabel, logKey, group, "files", len(files))
	}

	return nil
}

func groupDailyFile(name string) (string, bool) {
	if m := dailyPattern.FindStringSubmatch(name); m != nil {
		return m[1][:7], true
	}
	return "", false
}

func groupMonthlyFile(name string) (string, bool) {
	if m := monthlyPattern.FindStringSubmatch(name); m != nil {
		return m[1][:4], true
	}
	return "", false
}

func readAllAggregateFiles[T any](paths []string, read func(string) ([]T, error)) ([]T, error) {
	var all []T
	for _, p := range paths {
		items, err := read(p)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
	}
	return all, nil
}

func removeAggregateSources(paths []string) {
	for _, f := range paths {
		if err := os.Remove(f); err != nil {
			slog.Warn("remove source file", "path", f, "error", err)
		}
	}
}

func isMonthComplete(yearMonth string, now time.Time) bool {
	t, err := time.Parse("2006-01", yearMonth)
	if err != nil {
		return false
	}
	endOfMonth := t.AddDate(0, 1, 0)
	return now.After(endOfMonth)
}

func isYearComplete(year string, now time.Time) bool {
	t, err := time.Parse("2006", year)
	if err != nil {
		return false
	}
	endOfYear := t.AddDate(1, 0, 0)
	return now.After(endOfYear)
}

func dedup(records []source.Record) []source.Record {
	return dedupBy(records, dedupKey)
}

func dedupBy[T any](items []T, keyFor func(T) string) []T {
	seen := make(map[string]struct{})
	var result []T

	for _, item := range items {
		key := keyFor(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, item)
	}
	return result
}

func dedupKey(r source.Record) string {
	var b strings.Builder
	b.WriteString(r.Source)
	b.WriteByte('|')
	b.WriteString(r.ProjectID)
	b.WriteByte('|')
	b.WriteString(r.Metric)
	b.WriteByte('|')
	b.WriteString(r.Date)

	if len(r.Extra) > 0 {
		keys := make([]string, 0, len(r.Extra))
		for k := range r.Extra {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteByte('|')
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(r.Extra[k])
		}
	}

	return b.String()
}

func dedupEvents(events []source.Event) []source.Event {
	return dedupBy(events, dedupEventKey)
}

func dedupEventKey(e source.Event) string {
	var b strings.Builder
	b.WriteString(e.Source)
	b.WriteByte('|')
	b.WriteString(e.ProjectID)
	b.WriteByte('|')
	b.WriteString(e.Type)
	b.WriteByte('|')
	b.WriteString(e.Datetime)

	if e.User != "" {
		b.WriteByte('|')
		b.WriteString(e.User)
	}

	if len(e.Extra) > 0 {
		keys := make([]string, 0, len(e.Extra))
		for k := range e.Extra {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteByte('|')
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(e.Extra[k])
		}
	}

	return b.String()
}
