package store

import (
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/posit-dev/velocirepo/internal/source"
)

// metricWatermark records the latest date on which a target's total_* series
// were successfully fetched, regardless of whether any value changed. One entry
// is kept per target: every series under a target (e.g. all videos of a YouTube
// channel) is fetched together, so per-series granularity would only duplicate
// the same date across thousands of rows.
//
// Watermarks are stored in a single constant-named file per project
// (_watermark.json), co-located with the metric data and overwritten in place
// each fetch. It is mutable "last date checked" state, not an append log, so
// there is nothing to accumulate or aggregate.
type metricWatermark struct {
	Source    string `json:"source"`
	ProjectID string `json:"project_id"`
	Target    string `json:"target"`
	Date      string `json:"date"`
}

func writeMetricWatermarks(dataDir, sourceName, projectID, date string, records []source.Record) error {
	return mergeMetricWatermarks(dataDir, sourceName, projectID, metricWatermarks(sourceName, projectID, date, records))
}

// MarkFetched records that a project's targets have been fetched through date,
// even if the fetch found nothing to store. Event sources use it: a quiet
// repository has no events to show how far it was fetched, so without it
// every run would fetch the same quiet days again and its months would never
// be compacted.
func MarkFetched(dataDir, sourceName, projectID, date string, targets []string) error {
	updates := make([]metricWatermark, 0, len(targets))
	for _, target := range targets {
		updates = append(updates, metricWatermark{Source: sourceName, ProjectID: projectID, Target: target, Date: date})
	}
	return mergeMetricWatermarks(dataDir, sourceName, projectID, updates)
}

// mergeMetricWatermarks moves the given targets' watermarks forward. A
// watermark never moves backwards.
func mergeMetricWatermarks(dataDir, sourceName, projectID string, updates []metricWatermark) error {
	if len(updates) == 0 {
		return nil
	}

	path := WatermarkFilePath(dataDir, sourceName, projectID)

	existing, err := readMetricWatermarks(path)
	if err != nil {
		return err
	}

	byTarget := make(map[string]metricWatermark, len(existing)+len(updates))
	for _, w := range existing {
		byTarget[w.Target] = w
	}
	for _, w := range updates {
		if prev, ok := byTarget[w.Target]; !ok || w.Date > prev.Date {
			byTarget[w.Target] = w
		}
	}

	merged := make([]metricWatermark, 0, len(byTarget))
	for _, w := range byTarget {
		merged = append(merged, w)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Target < merged[j].Target })

	if err := os.MkdirAll(MetricsProjectDir(dataDir, sourceName, projectID), 0755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	return writeJSONLAtomic(path, merged, "metric watermark")
}

// MetricWatermark is the exported, per-target "last date checked" record for a
// project's total_* series. See metricWatermark for the semantics.
type MetricWatermark struct {
	Source    string
	ProjectID string
	Target    string
	Date      string
}

// ReadMetricWatermarks returns the per-target watermarks recorded for a
// project's metric source, sorted by target. It returns nil (and no error) when
// no watermark file exists.
func ReadMetricWatermarks(dataDir, sourceName, projectID string) ([]MetricWatermark, error) {
	internal, err := readMetricWatermarks(WatermarkFilePath(dataDir, sourceName, projectID))
	if err != nil {
		return nil, err
	}
	if len(internal) == 0 {
		return nil, nil
	}
	out := make([]MetricWatermark, len(internal))
	for i, w := range internal {
		out[i] = MetricWatermark(w)
	}
	return out, nil
}

func readMetricWatermarks(path string) ([]metricWatermark, error) {
	watermarks, err := readJSONL[metricWatermark](path, readJSONLOptions{wrapErrors: true})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return watermarks, nil
}

func metricWatermarks(sourceName, projectID, date string, records []source.Record) []metricWatermark {
	seen := make(map[string]metricWatermark)
	for _, r := range records {
		if !isTotalMetric(r.Metric) {
			continue
		}
		if _, ok := seen[r.Target]; ok {
			continue
		}
		seen[r.Target] = metricWatermark{
			Source:    sourceName,
			ProjectID: projectID,
			Target:    r.Target,
			Date:      date,
		}
	}
	if len(seen) == 0 {
		return nil
	}

	targets := make([]string, 0, len(seen))
	for target := range seen {
		targets = append(targets, target)
	}
	sort.Strings(targets)

	watermarks := make([]metricWatermark, 0, len(targets))
	for _, target := range targets {
		watermarks = append(watermarks, seen[target])
	}
	return watermarks
}
