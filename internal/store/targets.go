package store

import (
	"os"
	"path/filepath"
)

// TargetsWithMetric returns the targets of a source's project that have at
// least one record of metric stored.
func TargetsWithMetric(dataDir, sourceName, projectID, metric string) (map[string]bool, error) {
	dir := MetricsProjectDir(dataDir, sourceName, projectID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	targets := make(map[string]bool)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, _, _, ok := periodSpan(e.Name()); !ok {
			continue
		}
		records, err := ReadRecords(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		for _, r := range records {
			if r.Metric == metric {
				targets[r.Target] = true
			}
		}
	}
	return targets, nil
}
