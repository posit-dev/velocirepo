package store

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/posit-dev/velocirepo/internal/dateutil"
	"github.com/posit-dev/velocirepo/internal/source"
)

func isTotalMetric(metric string) bool {
	return strings.HasPrefix(metric, "total_")
}

func totalKey(r source.Record) string {
	var b strings.Builder
	b.WriteString(r.Metric)
	b.WriteByte('|')
	b.WriteString(r.Target)
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

func lastRecordedTotals(dir string) (map[string]int64, error) {
	return lastRecordedTotalsFor(dir, nil, "")
}

// lastRecordedTotalsFor returns the latest recorded value of each wanted
// total_* series (every series if wantedKeys is empty), counting only records
// dated before `before` ("" counts everything). Comparing a day's values with
// records of that same day would drop its change points when the day is
// fetched again. For the same date, a daily file wins over a period file: it
// was written later.
func lastRecordedTotalsFor(dir string, wantedKeys map[string]struct{}, before string) (map[string]int64, error) {
	states, err := lastRecordedTotalStates(dir, wantedKeys, before)
	if err != nil || len(states) == 0 {
		return nil, err
	}
	values := make(map[string]int64, len(states))
	for key, state := range states {
		values[key] = state.value
	}
	return values, nil
}

// totalState is the latest recorded value of a total_* series, and its date.
type totalState struct {
	value int64
	date  string
	daily bool
}

// lastRecordedTotalStates is lastRecordedTotalsFor with the dates of the
// values.
func lastRecordedTotalStates(dir string, wantedKeys map[string]struct{}, before string) (map[string]totalState, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	type fileSpan struct {
		name        string
		first, last string
		daily       bool
	}
	var files []fileSpan
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		first, last, daily, ok := periodSpan(e.Name())
		if !ok || (before != "" && first >= before) {
			continue
		}
		files = append(files, fileSpan{name: e.Name(), first: first, last: last, daily: daily})
	}
	if len(files) == 0 {
		return nil, nil
	}

	// Newest period first, so the search can stop early.
	sort.Slice(files, func(i, j int) bool {
		if files[i].last != files[j].last {
			return files[i].last > files[j].last
		}
		return files[i].daily && !files[j].daily
	})

	totals := make(map[string]totalState)
	for _, f := range files {
		if len(wantedKeys) > 0 && len(totals) == len(wantedKeys) {
			// Every wanted series has a value newer than anything this file
			// (and every file after it) can hold.
			done := true
			for _, total := range totals {
				if total.date <= f.last {
					done = false
					break
				}
			}
			if done {
				break
			}
		}

		records, err := ReadRecords(filepath.Join(dir, f.name))
		if err != nil {
			continue
		}
		for _, r := range records {
			if !isTotalMetric(r.Metric) || (before != "" && r.Date >= before) {
				continue
			}
			key := totalKey(r)
			if len(wantedKeys) > 0 {
				if _, ok := wantedKeys[key]; !ok {
					continue
				}
			}
			prev, exists := totals[key]
			if !exists || r.Date > prev.date || (r.Date == prev.date && f.daily && !prev.daily) {
				totals[key] = totalState{value: r.Value, date: r.Date, daily: f.daily}
			}
		}
	}

	if len(totals) == 0 {
		return nil, nil
	}
	return totals, nil
}

// periodSpan returns the first and last date a data file can hold, judging by
// its name, and whether it is a daily file.
func periodSpan(name string) (first, last string, daily, ok bool) {
	if m := dailyPattern.FindStringSubmatch(name); m != nil {
		return m[1], m[1], true, true
	}
	if m := monthlyPattern.FindStringSubmatch(name); m != nil {
		return m[1] + "-01", dateutil.LastDayOfMonth(m[1]), false, true
	}
	if m := yearlyPattern.FindStringSubmatch(name); m != nil {
		return m[1] + "-01-01", m[1] + "-12-31", false, true
	}
	return "", "", false, false
}

func filterUnchangedTotals(records []source.Record, lastValues map[string]int64) []source.Record {
	if len(lastValues) == 0 {
		return records
	}

	result := make([]source.Record, 0, len(records))
	for _, r := range records {
		if !isTotalMetric(r.Metric) {
			result = append(result, r)
			continue
		}
		key := totalKey(r)
		prev, exists := lastValues[key]
		if !exists || r.Value != prev {
			result = append(result, r)
		}
	}
	return result
}

func totalKeys(records []source.Record) map[string]struct{} {
	keys := make(map[string]struct{})
	for _, r := range records {
		if isTotalMetric(r.Metric) {
			keys[totalKey(r)] = struct{}{}
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return keys
}
