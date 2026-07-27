package store

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

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
	return lastRecordedTotalsFor(dir, nil)
}

func lastRecordedTotalsFor(dir string, wantedKeys map[string]struct{}) (map[string]int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		files = append(files, e.Name())
	}
	if len(files) == 0 {
		return nil, nil
	}

	sort.Strings(files)

	type totalState struct {
		value int64
		date  string
	}

	totals := make(map[string]totalState)
	for i := len(files) - 1; i >= 0; i-- {
		path := filepath.Join(dir, files[i])
		records, err := ReadRecords(path)
		if err != nil {
			continue
		}
		for _, r := range records {
			if isTotalMetric(r.Metric) {
				key := totalKey(r)
				if len(wantedKeys) > 0 {
					if _, ok := wantedKeys[key]; !ok {
						continue
					}
				}
				prev, exists := totals[key]
				if !exists || r.Date > prev.date {
					totals[key] = totalState{value: r.Value, date: r.Date}
				}
			}
		}
		if len(wantedKeys) > 0 && len(totals) == len(wantedKeys) {
			break
		}
	}

	if len(totals) == 0 {
		return nil, nil
	}

	values := make(map[string]int64, len(totals))
	for key, total := range totals {
		values[key] = total.value
	}
	return values, nil
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
