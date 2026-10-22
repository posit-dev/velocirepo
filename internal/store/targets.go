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

// LatestMetricDates returns, per target, the latest date a metric is stored
// for.
func LatestMetricDates(dataDir, sourceName, projectID, metric string) (map[string]string, error) {
	dir := MetricsProjectDir(dataDir, sourceName, projectID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	latest := make(map[string]string)
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
			if r.Metric == metric && r.Date > latest[r.Target] {
				latest[r.Target] = r.Date
			}
		}
	}
	return latest, nil
}

// TargetHasData reports whether anything is stored for a target of a source
// and project: records, events or content.
func TargetHasData(dataDir, sourceName, projectID, target string) (bool, error) {
	for _, dir := range []string{MetricsProjectDir(dataDir, sourceName, projectID), EventsProjectDir(dataDir, sourceName, projectID)} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if _, _, _, ok := periodSpan(e.Name()); !ok {
				continue
			}
			path := filepath.Join(dir, e.Name())
			if dir == EventsProjectDir(dataDir, sourceName, projectID) {
				events, err := ReadEvents(path)
				if err != nil {
					return false, err
				}
				for _, ev := range events {
					if ev.Target == target {
						return true, nil
					}
				}
				continue
			}
			records, err := ReadRecords(path)
			if err != nil {
				return false, err
			}
			for _, r := range records {
				if r.Target == target {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
