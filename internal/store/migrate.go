package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/posit-dev/velocirepo/internal/sourceinfo"
)

const LatestSchemaVersion = 7

const schemaVersionFile = ".schema-version"

func SchemaVersion(dataDir string) (int, error) {
	path := filepath.Join(dataDir, schemaVersionFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse schema version: %w", err)
	}
	return v, nil
}

func writeSchemaVersion(dataDir string, version int) error {
	path := filepath.Join(dataDir, schemaVersionFile)
	return os.WriteFile(path, []byte(strconv.Itoa(version)+"\n"), 0644)
}

func ensureSchemaVersion(dataDir string) {
	path := filepath.Join(dataDir, schemaVersionFile)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		_ = writeSchemaVersion(dataDir, LatestSchemaVersion)
	}
}

func CheckSchemaVersion(dataDir string) error {
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		return nil
	}
	v, err := SchemaVersion(dataDir)
	if err != nil {
		return err
	}
	if v < LatestSchemaVersion {
		return fmt.Errorf("data schema is at version %d, but version %d is required; run `velocirepo migrate` to update", v, LatestSchemaVersion)
	}
	if v > LatestSchemaVersion {
		return fmt.Errorf("data schema is at version %d, but this binary only supports up to version %d; update velocirepo", v, LatestSchemaVersion)
	}
	return nil
}

type migration struct {
	description string
	run         func(dataDir string) error
}

var migrations = []migration{
	{
		description: "prefix metric names with daily_ or total_",
		run:         migrate0to1,
	},
	{
		description: "pluralize metric names",
		run:         migrate1to2,
	},
	{
		description: "rename site-level plausible metrics to daily_site_*",
		run:         migrate2to3,
	},
	{
		description: "rename github event fields: event_type→type, github_repo→target, user→tags",
		run:         migrate3to4,
	},
	{
		description: "move youtube index to data/content/",
		run:         migrate4to5,
	},
	{
		description: "collapse data/watermarks/ tree into per-project _watermark.json",
		run:         migrate5to6,
	},
	{
		description: "rename tags→extra in events/metrics, promote user to top-level in events",
		run:         migrate6to7,
	},
}

func Migrate(dataDir string) (int, error) {
	current, err := SchemaVersion(dataDir)
	if err != nil {
		return 0, err
	}
	return MigrateFrom(dataDir, current)
}

func MigrateFrom(dataDir string, fromVersion int) (int, error) {
	if fromVersion >= LatestSchemaVersion {
		return 0, nil
	}

	applied := 0
	for i := fromVersion; i < LatestSchemaVersion; i++ {
		m := migrations[i]
		if err := m.run(dataDir); err != nil {
			return applied, fmt.Errorf("migration %d (%s): %w", i+1, m.description, err)
		}
		if err := writeSchemaVersion(dataDir, i+1); err != nil {
			return applied, fmt.Errorf("write schema version after migration %d: %w", i+1, err)
		}
		applied++
	}
	return applied, nil
}

func MigrationDescription(version int) string {
	if version < 1 || version > len(migrations) {
		return ""
	}
	return migrations[version-1].description
}

var metricPrefixRenames = map[string]string{
	"downloads":     "daily_downloads",
	"views":         "daily_views",
	"unique_views":  "daily_unique_views",
	"clones":        "daily_clones",
	"unique_clones": "daily_unique_clones",
	"pageviews":     "daily_pageviews",
	"visitors":      "daily_visitors",
	"visits":        "daily_visits",
	"reviews":       "total_reviews",
	"rating":        "total_rating",
	"subscribers":   "total_subscribers",
	"channel_views": "total_channel_views",
	"video_count":   "total_video_count",
	"likes":         "total_likes",
	"comments":      "total_comments",
}

var metricPluralRenames = map[string]string{
	"total_rating":      "total_ratings",
	"total_video_count": "total_videos",
}

var metricSiteRenames = map[string]string{
	"daily_pageviews": "daily_site_pageviews",
	"daily_visitors":  "daily_site_visitors",
	"daily_visits":    "daily_site_visits",
}

func migrate0to1(dataDir string) error {
	sourceDirs := []string{"pypi", "cran", "github-traffic", "plausible", "openvsx", "youtube"}
	for _, src := range sourceDirs {
		dir := filepath.Join(dataDir, src)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		if err := renameMetricsInDir(dir, metricPrefixRenames); err != nil {
			return fmt.Errorf("%s: %w", src, err)
		}
	}
	return nil
}

func migrate1to2(dataDir string) error {
	sourceDirs := []string{"openvsx", "youtube"}
	for _, src := range sourceDirs {
		dir := filepath.Join(dataDir, src)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		if err := renameMetricsInDir(dir, metricPluralRenames); err != nil {
			return fmt.Errorf("%s: %w", src, err)
		}
	}
	return nil
}

func migrate2to3(dataDir string) error {
	dir := filepath.Join(dataDir, "plausible")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	return renameMetricsInDir(dir, metricSiteRenames)
}

func renameMetricsInDir(dir string, renames map[string]string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".jsonl") {
			return nil
		}
		return renameMetricsInFile(path, renames)
	})
}

func migrate3to4(dataDir string) error {
	// Rewrite github event fields in place first
	githubDir := filepath.Join(dataDir, "github")
	if _, err := os.Stat(githubDir); err == nil {
		if err := filepath.Walk(githubDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(info.Name(), ".jsonl") {
				return nil
			}
			return rewriteEventFields(path)
		}); err != nil {
			return err
		}
	}

	// Move source directories into metrics/ and events/ subdirs
	eventsDir := filepath.Join(dataDir, EventsDir)
	metricsDir := filepath.Join(dataDir, MetricsDir)

	for _, src := range sourceinfo.EventNames() {
		srcDir := filepath.Join(dataDir, src)
		if _, err := os.Stat(srcDir); os.IsNotExist(err) {
			continue
		}
		if err := os.MkdirAll(eventsDir, 0755); err != nil {
			return fmt.Errorf("create events dir: %w", err)
		}
		dst := filepath.Join(eventsDir, src)
		if err := os.Rename(srcDir, dst); err != nil {
			return fmt.Errorf("move %s to events/: %w", src, err)
		}
	}

	for _, src := range sourceinfo.MetricNames() {
		srcDir := filepath.Join(dataDir, src)
		if _, err := os.Stat(srcDir); os.IsNotExist(err) {
			continue
		}
		if err := os.MkdirAll(metricsDir, 0755); err != nil {
			return fmt.Errorf("create metrics dir: %w", err)
		}
		dst := filepath.Join(metricsDir, src)
		if err := os.Rename(srcDir, dst); err != nil {
			return fmt.Errorf("move %s to metrics/: %w", src, err)
		}
	}

	return nil
}

func rewriteEventFields(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var lines [][]byte
	modified := false
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var record map[string]interface{}
		if err := json.Unmarshal(line, &record); err != nil {
			lines = append(lines, append([]byte(nil), line...))
			continue
		}

		changed := false

		if v, ok := record["event_type"]; ok {
			record["type"] = v
			delete(record, "event_type")
			changed = true
		}

		if v, ok := record["github_repo"]; ok {
			record["target"] = v
			delete(record, "github_repo")
			changed = true
		}

		if v, ok := record["user"]; ok {
			user, _ := v.(string)
			delete(record, "user")
			if user != "" {
				tags, _ := record["tags"].(map[string]interface{})
				if tags == nil {
					tags = make(map[string]interface{})
				}
				tags["user"] = user
				record["tags"] = tags
			}
			changed = true
		}

		if !changed {
			lines = append(lines, append([]byte(nil), line...))
			continue
		}

		newLine, err := json.Marshal(record)
		if err != nil {
			lines = append(lines, append([]byte(nil), line...))
			continue
		}
		lines = append(lines, newLine)
		modified = true
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	_ = f.Close()

	if !modified {
		return nil
	}

	return writeJSONLLinesAtomic(path, lines)
}

func migrate4to5(dataDir string) error {
	youtubeDir := filepath.Join(dataDir, MetricsDir, "youtube")
	if _, err := os.Stat(youtubeDir); os.IsNotExist(err) {
		return nil
	}

	projDirs, err := os.ReadDir(youtubeDir)
	if err != nil {
		return fmt.Errorf("read youtube dir: %w", err)
	}

	for _, projEntry := range projDirs {
		if !projEntry.IsDir() {
			continue
		}
		projID := projEntry.Name()
		indexPath := filepath.Join(youtubeDir, projID, "index.jsonl")

		if _, err := os.Stat(indexPath); os.IsNotExist(err) {
			continue
		}

		entries, err := readYouTubeIndex(indexPath)
		if err != nil {
			return fmt.Errorf("read index for %s: %w", projID, err)
		}

		contentDir := ContentProjectDir(dataDir, "youtube", projID)
		if err := os.MkdirAll(contentDir, 0755); err != nil {
			return fmt.Errorf("create content dir for %s: %w", projID, err)
		}

		contentPath := filepath.Join(contentDir, "videos.jsonl")
		contentEntries := make([]contentEntry5, 0, len(entries))
		for _, e := range entries {
			var dur *int64
			if e.Duration != 0 {
				d := e.Duration
				dur = &d
			}
			ce := contentEntry5{
				Source:      "youtube",
				Target:      e.Channel,
				ID:          e.VideoID,
				Title:       e.Title,
				PublishedAt: e.PublishedAt,
				URL:         "https://youtube.com/watch?v=" + e.VideoID,
				Duration:    dur,
				Tags:        e.Tags,
				Type:        "video",
			}
			contentEntries = append(contentEntries, ce)
		}

		if err := writeJSONLAtomic(contentPath, contentEntries, "content entry"); err != nil {
			return err
		}

		_ = os.Remove(indexPath)
	}

	return nil
}

// oldMetricWatermark is the pre-v6 watermark record shape, stored one row per
// total-series key in dated files under data/watermarks/metrics/. Only the
// fields needed to derive the per-target horizon are decoded.
type oldMetricWatermark struct {
	Source    string `json:"source"`
	ProjectID string `json:"project_id"`
	Target    string `json:"target"`
	Date      string `json:"date"`
}

// migrate5to6 collapses the parallel data/watermarks/metrics/<source>/<project>/
// *.jsonl tree into a single _watermark.json per project, co-located with the
// metric data. Old records are per-series; the new format keeps one entry per
// target at its latest observed date. The old tree is removed once migrated.
func migrate5to6(dataDir string) error {
	oldRoot := filepath.Join(dataDir, "watermarks", "metrics")
	if _, err := os.Stat(oldRoot); os.IsNotExist(err) {
		return nil
	}

	sourceDirs, err := os.ReadDir(oldRoot)
	if err != nil {
		return fmt.Errorf("read watermarks dir: %w", err)
	}

	for _, sourceEntry := range sourceDirs {
		if !sourceEntry.IsDir() {
			continue
		}
		sourceName := sourceEntry.Name()
		sourceDir := filepath.Join(oldRoot, sourceName)

		projEntries, err := os.ReadDir(sourceDir)
		if err != nil {
			return fmt.Errorf("read %s: %w", sourceDir, err)
		}

		for _, projEntry := range projEntries {
			if !projEntry.IsDir() {
				continue
			}
			projID := projEntry.Name()
			if err := migrateWatermarkProject(dataDir, sourceName, projID, filepath.Join(sourceDir, projID)); err != nil {
				return err
			}
		}
	}

	// Remove the now-empty (or fully migrated) old tree.
	if err := os.RemoveAll(filepath.Join(dataDir, "watermarks")); err != nil {
		return fmt.Errorf("remove old watermarks tree: %w", err)
	}
	return nil
}

// migrateWatermarkProject reads every dated *.jsonl watermark file for one
// project, keeps the latest date per target, and writes the collapsed result to
// the co-located _watermark.json.
func migrateWatermarkProject(dataDir, sourceName, projID, oldProjDir string) error {
	files, err := os.ReadDir(oldProjDir)
	if err != nil {
		return fmt.Errorf("read %s: %w", oldProjDir, err)
	}

	byTarget := make(map[string]metricWatermark)

	// Seed from any existing collapsed _watermark.json first. The pre-v6
	// collapsed-watermark code (commit d02baf2) shipped under schema v5 without
	// bumping the version, so a v5 data dir can already hold newer per-target
	// horizons alongside a leftover old tree. Merge by max date so migrating
	// never regresses a target to an older legacy date.
	existing, err := readMetricWatermarks(WatermarkFilePath(dataDir, sourceName, projID))
	if err != nil {
		return err
	}
	for _, w := range existing {
		if w.Target == "" || w.Date == "" {
			continue
		}
		byTarget[w.Target] = metricWatermark{
			Source:    sourceName,
			ProjectID: projID,
			Target:    w.Target,
			Date:      w.Date,
		}
	}

	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
			continue
		}
		olds, err := readJSONL[oldMetricWatermark](filepath.Join(oldProjDir, f.Name()), readJSONLOptions{wrapErrors: true})
		if err != nil {
			return err
		}
		for _, o := range olds {
			if o.Target == "" || o.Date == "" {
				continue
			}
			if prev, ok := byTarget[o.Target]; !ok || o.Date > prev.Date {
				byTarget[o.Target] = metricWatermark{
					Source:    sourceName,
					ProjectID: projID,
					Target:    o.Target,
					Date:      o.Date,
				}
			}
		}
	}

	if len(byTarget) == 0 {
		return nil
	}

	merged := make([]metricWatermark, 0, len(byTarget))
	for _, w := range byTarget {
		merged = append(merged, w)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Target < merged[j].Target })

	if err := os.MkdirAll(MetricsProjectDir(dataDir, sourceName, projID), 0755); err != nil {
		return fmt.Errorf("create metrics dir: %w", err)
	}
	return writeJSONLAtomic(WatermarkFilePath(dataDir, sourceName, projID), merged, "metric watermark")
}

type contentEntry5 struct {
	Source      string   `json:"source"`
	Target      string   `json:"target"`
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	PublishedAt string   `json:"published_at"`
	URL         string   `json:"url,omitempty"`
	Duration    *int64   `json:"duration,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Type        string   `json:"type,omitempty"`
}

func migrate6to7(dataDir string) error {
	eventsDir := filepath.Join(dataDir, EventsDir)
	if _, err := os.Stat(eventsDir); err == nil {
		if err := filepath.Walk(eventsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(info.Name(), ".jsonl") || info.Name() == WatermarkFileName {
				return nil
			}
			return migrateEventTagsToExtra(path)
		}); err != nil {
			return fmt.Errorf("events: %w", err)
		}
	}

	metricsDir := filepath.Join(dataDir, MetricsDir)
	if _, err := os.Stat(metricsDir); err == nil {
		if err := filepath.Walk(metricsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(info.Name(), ".jsonl") || info.Name() == WatermarkFileName {
				return nil
			}
			return renameFieldInFile(path, "tags", "extra")
		}); err != nil {
			return fmt.Errorf("metrics: %w", err)
		}
	}

	return nil
}

func migrateEventTagsToExtra(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var lines [][]byte
	modified := false
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var record map[string]interface{}
		if err := json.Unmarshal(line, &record); err != nil {
			lines = append(lines, append([]byte(nil), line...))
			continue
		}

		changed := false

		if tags, ok := record["tags"].(map[string]interface{}); ok {
			if user, ok := tags["user"].(string); ok && user != "" {
				record["user"] = user
				delete(tags, "user")
				changed = true
			}
			if len(tags) > 0 {
				record["extra"] = tags
			}
			delete(record, "tags")
			changed = true
		} else if _, hasTags := record["tags"]; hasTags {
			delete(record, "tags")
			changed = true
		}

		if !changed {
			lines = append(lines, append([]byte(nil), line...))
			continue
		}

		newLine, err := json.Marshal(record)
		if err != nil {
			lines = append(lines, append([]byte(nil), line...))
			continue
		}
		lines = append(lines, newLine)
		modified = true
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	_ = f.Close()

	if !modified {
		return nil
	}

	return writeJSONLLinesAtomic(path, lines)
}

func renameFieldInFile(path, oldName, newName string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var lines [][]byte
	modified := false
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var record map[string]interface{}
		if err := json.Unmarshal(line, &record); err != nil {
			lines = append(lines, append([]byte(nil), line...))
			continue
		}

		if v, ok := record[oldName]; ok {
			record[newName] = v
			delete(record, oldName)
			newLine, err := json.Marshal(record)
			if err != nil {
				lines = append(lines, append([]byte(nil), line...))
				continue
			}
			lines = append(lines, newLine)
			modified = true
			continue
		}
		lines = append(lines, append([]byte(nil), line...))
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	_ = f.Close()

	if !modified {
		return nil
	}

	return writeJSONLLinesAtomic(path, lines)
}

func renameMetricsInFile(path string, renames map[string]string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var lines [][]byte
	modified := false
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var record map[string]interface{}
		if err := json.Unmarshal(line, &record); err != nil {
			lines = append(lines, append([]byte(nil), line...))
			continue
		}

		metric, ok := record["metric"].(string)
		if ok {
			if newName, found := renames[metric]; found {
				record["metric"] = newName
				newLine, err := json.Marshal(record)
				if err != nil {
					lines = append(lines, append([]byte(nil), line...))
					continue
				}
				lines = append(lines, newLine)
				modified = true
				continue
			}
		}
		lines = append(lines, append([]byte(nil), line...))
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	_ = f.Close()

	if !modified {
		return nil
	}

	return writeJSONLLinesAtomic(path, lines)
}
