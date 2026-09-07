package store

import "path/filepath"

const (
	MetricsDir     = "metrics"
	EventsDir      = "events"
	ContentDataDir = "content"
	LogDir         = "log"

	// WatermarkFileName is the constant name of the per-project watermark file,
	// co-located with the metric data. It is intentionally not a *.jsonl file so
	// the metric globs and directory scans skip it automatically.
	WatermarkFileName = "_watermark.json"
)

func MetricsProjectDir(dataDir, sourceName, projectID string) string {
	return filepath.Join(dataDir, MetricsDir, sourceName, projectID)
}

func EventsProjectDir(dataDir, sourceName, projectID string) string {
	return filepath.Join(dataDir, EventsDir, sourceName, projectID)
}

func ContentProjectDir(dataDir, sourceName, projectID string) string {
	return filepath.Join(dataDir, ContentDataDir, sourceName, projectID)
}

func SourceProjectDir(dataDir, sourceName, projectID string) string {
	return filepath.Join(dataDir, SourceCategory(sourceName), sourceName, projectID)
}

func WatermarkFilePath(dataDir, sourceName, projectID string) string {
	return filepath.Join(MetricsProjectDir(dataDir, sourceName, projectID), WatermarkFileName)
}

func LogFilePath(dataDir, date string) string {
	return filepath.Join(dataDir, LogDir, date+".jsonl")
}
