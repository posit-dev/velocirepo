package store

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/posit-dev/velocirepo/internal/dateutil"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/sourceinfo"
)

// SourceCategory returns the date-partitioned category ("metrics" or "events")
// for a given source. Sources may also produce content via the ContentProvider
// interface, which is handled separately by the fetch orchestrator.
func SourceCategory(sourceName string) string {
	return sourceinfo.CategoryDir(sourceName)
}

// Aggregate concatenates daily→monthly→yearly for all projects. A period is
// concatenated once it has ended and its last day has been stored, so a day
// that is still missing (a failed fetch, a source that publishes late) is
// fetched by the next run instead of being treated as done. A project that
// fails doesn't stop the others; the errors are returned together. Category
// directories that don't exist yet are skipped.
func Aggregate(dataDir string, now time.Time) error {
	return AggregateWithPolicy(dataDir, now, Policy{})
}

// Policy says when a period is concatenated, on top of it having ended.
type Policy struct {
	// Day is the day of the following month from which a month is
	// concatenated, so that late corrections of its last days still go into
	// daily files. A year is concatenated with its December. Zero means as
	// soon as the period has ended.
	Day int
	// Ready reports whether a project's data is complete through lastDay,
	// the period's last day. Nil means it's complete once data has been
	// stored through that day.
	Ready func(category, sourceName, projectID, lastDay string) bool
}

// AggregateWithPolicy is Aggregate with a policy for when periods are
// concatenated.
func AggregateWithPolicy(dataDir string, now time.Time, policy Policy) error {
	return errors.Join(
		aggregateCategory(dataDir, MetricsDir, now, policy, recordAggregateSpec()),
		aggregateCategory(dataDir, EventsDir, now, policy, eventAggregateSpec()),
	)
}

// onCalendar reports whether the month following yearMonth has reached the
// policy's day.
func (p Policy) onCalendar(yearMonth string, now time.Time) bool {
	if p.Day <= 1 {
		return true
	}
	t, err := time.Parse("2006-01", yearMonth)
	if err != nil {
		return false
	}
	from := t.AddDate(0, 1, p.Day-1)
	return !now.Before(from)
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
		dedup: func(records []source.Record) []source.Record {
			return dedupNewest(records, dedupKey)
		},
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
		dedup: func(events []source.Event) []source.Event {
			return dedupNewest(events, dedupEventKey)
		},
		less: func(a, b source.Event) bool {
			return a.Datetime < b.Datetime
		},
		label: "events",
	}
}

func aggregateCategory[T any](dataDir, category string, now time.Time, policy Policy, spec aggregateSpec[T]) error {
	categoryDir := filepath.Join(dataDir, category)
	sourceDirs, err := os.ReadDir(categoryDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var errs []error
	for _, sourceEntry := range sourceDirs {
		if !sourceEntry.IsDir() {
			continue
		}
		sourcePath := filepath.Join(categoryDir, sourceEntry.Name())

		projectDirs, err := os.ReadDir(sourcePath)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		for _, projEntry := range projectDirs {
			if !projEntry.IsDir() {
				continue
			}
			projPath := filepath.Join(sourcePath, projEntry.Name())
			watermarkPath := WatermarkFilePath(dataDir, sourceEntry.Name(), projEntry.Name())
			var ready func(string) bool
			if policy.Ready != nil {
				sourceName, projectID := sourceEntry.Name(), projEntry.Name()
				ready = func(lastDay string) bool { return policy.Ready(category, sourceName, projectID, lastDay) }
			}
			if err := aggregateProject(projPath, watermarkPath, now, policy, ready, spec); err != nil {
				errs = append(errs, fmt.Errorf("concatenate %s: %w", projPath, err))
			}
		}
	}
	return errors.Join(errs...)
}

// aggregateProject concatenates the complete months and years of one
// project directory. A period counts as complete once it has ended, the
// policy's day has come, and the project's data is complete through its last
// day. That keeps LastDate's reading of a period file as "fetched through the
// period's last day" true, and leaves a period with a missing last day as
// daily files until the next fetch fills it.
func aggregateProject[T any](projDir, watermarkPath string, now time.Time, policy Policy, ready func(string) bool, spec aggregateSpec[T]) error {
	complete := ready
	if complete == nil {
		through, err := latestStoredDate(projDir, watermarkPath)
		if err != nil {
			return err
		}
		complete = func(lastDay string) bool { return through >= lastDay }
	}

	monthComplete := func(month string, now time.Time) bool {
		return isMonthComplete(month, now) && policy.onCalendar(month, now) && complete(dateutil.LastDayOfMonth(month))
	}
	if err := aggregateFiles(projDir, now, spec, groupDailyFile, monthComplete, "month", "daily to monthly"); err != nil {
		return err
	}

	yearComplete := func(year string, now time.Time) bool {
		return isYearComplete(year, now) && policy.onCalendar(year+"-12", now) && complete(year+"-12-31")
	}
	return aggregateFiles(projDir, now, spec, groupMonthlyFile, yearComplete, "year", "monthly to yearly")
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

	groups := make([]string, 0, len(groupedFiles))
	for group := range groupedFiles {
		groups = append(groups, group)
	}
	sort.Strings(groups)

	for _, group := range groups {
		if !isComplete(group, now) {
			continue
		}

		files := groupedFiles[group]
		sort.Strings(files)

		// A period file can already exist: data for an already-compacted period
		// arrives through backfills and repairs, and a crash can interrupt the
		// deletion of the inputs. Merge into it rather than replacing it. It is
		// read first so that the newer inputs win for records with the same key.
		outPath := filepath.Join(projDir, group+".jsonl")
		inputs := files
		if _, err := os.Stat(outPath); err == nil {
			inputs = append([]string{outPath}, files...)
		} else if !os.IsNotExist(err) {
			return err
		}

		items, err := readAllAggregateFiles(inputs, spec.read)
		if err != nil {
			return fmt.Errorf("read %s files for %s: %w", spec.label, group, err)
		}

		items = spec.dedup(items)
		sort.SliceStable(items, func(i, j int) bool {
			return spec.less(items[i], items[j])
		})

		if err := spec.write(outPath, items); err != nil {
			return err
		}

		if err := removeAggregateSources(files); err != nil {
			return err
		}

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

// removeAggregateSources deletes the inputs of a period file that has been
// written. A failure leaves the period and its inputs side by side, which the
// next compaction merges again, but it is reported.
func removeAggregateSources(paths []string) error {
	var errs []error
	for _, f := range paths {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("remove compacted file: %w", err))
		}
	}
	return errors.Join(errs...)
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

// dedupNewest keeps one item per key like dedupBy, but takes the value of the
// last occurrence. Compaction reads its inputs oldest first, so a re-fetched
// value replaces the one it corrects.
func dedupNewest[T any](items []T, keyFor func(T) string) []T {
	index := make(map[string]int, len(items))
	result := make([]T, 0, len(items))

	for _, item := range items {
		key := keyFor(item)
		if i, ok := index[key]; ok {
			result[i] = item
			continue
		}
		index[key] = len(result)
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
	b.WriteString(r.Target)
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
	b.WriteString(e.Target)
	b.WriteByte('|')
	b.WriteString(e.Type)
	b.WriteByte('|')
	b.WriteString(e.Datetime)
	b.WriteByte('|')
	if e.Ref != nil {
		b.WriteString(strconv.Itoa(*e.Ref))
	}

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
