package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/posit-dev/velocirepo/internal/dateutil"
	"github.com/posit-dev/velocirepo/internal/source"
)

type IssueType int

const (
	IssueMalformedJSON IssueType = iota
	IssueInvalidDate
	IssueDateMismatch
	IssueEmptyField
	IssueDuplicate
	IssueOrphanDir
	IssueInvalidDatetime
	IssueUnexpectedFile
	IssueSourceMismatch
	IssueDeprecatedMetric
	IssueProjectMismatch
	IssuePeriodOverlap
	IssueLeftoverTempFile
)

func (t IssueType) String() string {
	switch t {
	case IssueMalformedJSON:
		return "malformed JSON"
	case IssueInvalidDate:
		return "invalid date"
	case IssueDateMismatch:
		return "date mismatch"
	case IssueEmptyField:
		return "empty field"
	case IssueDuplicate:
		return "duplicate"
	case IssueOrphanDir:
		return "orphan directory"
	case IssueInvalidDatetime:
		return "invalid datetime"
	case IssueUnexpectedFile:
		return "unexpected file"
	case IssueSourceMismatch:
		return "source mismatch"
	case IssueDeprecatedMetric:
		return "deprecated metric"
	case IssueProjectMismatch:
		return "project mismatch"
	case IssuePeriodOverlap:
		return "period overlap"
	case IssueLeftoverTempFile:
		return "leftover temp file"
	default:
		return "unknown"
	}
}

type Issue struct {
	Type    IssueType
	Path    string
	Line    int
	Message string
	Fixable bool
}

type ValidationResult struct {
	Issues      []Issue
	FilesRead   int
	LinesRead   int
	RecordCount int
}

var datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func ValidateData(dataDir string, projectIDs map[string]bool) (*ValidationResult, error) {
	result := &ValidationResult{}

	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		return result, nil
	}

	validateCategory(filepath.Join(dataDir, MetricsDir), projectIDs, false, result)
	validateCategory(filepath.Join(dataDir, EventsDir), projectIDs, true, result)
	validateContentCategory(filepath.Join(dataDir, ContentDataDir), projectIDs, result)

	return result, nil
}

func validateCategory(categoryDir string, projectIDs map[string]bool, isEvents bool, result *ValidationResult) {
	sourceDirs, err := os.ReadDir(categoryDir)
	if err != nil {
		return
	}

	for _, sourceEntry := range sourceDirs {
		if !sourceEntry.IsDir() {
			continue
		}
		sourceName := sourceEntry.Name()
		sourcePath := filepath.Join(categoryDir, sourceName)

		projDirs, err := os.ReadDir(sourcePath)
		if err != nil {
			continue
		}

		for _, projEntry := range projDirs {
			if !projEntry.IsDir() {
				continue
			}
			projID := projEntry.Name()
			projPath := filepath.Join(sourcePath, projID)

			if projectIDs != nil && !projectIDs[projID] {
				result.Issues = append(result.Issues, Issue{
					Type:    IssueOrphanDir,
					Path:    projPath,
					Message: fmt.Sprintf("directory for unknown project %q (source: %s)", projID, sourceName),
					Fixable: true,
				})
				continue
			}

			if isEvents {
				validateEventsDir(projPath, result)
			} else {
				validateRecordsDir(projPath, sourceName, projID, result)
			}
		}
	}
}

func validateRecordsDir(dir, sourceName, projectID string, result *ValidationResult) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if IsWatermarkFile(name) {
			continue
		}
		path := filepath.Join(dir, name)

		if isLeftoverTempFile(name) {
			result.Issues = append(result.Issues, leftoverTempFileIssue(path))
			continue
		}

		fileDateRange := extractDateRange(name)
		if fileDateRange == "" {
			result.Issues = append(result.Issues, Issue{
				Type:    IssueUnexpectedFile,
				Path:    path,
				Message: fmt.Sprintf("file %q does not match expected naming pattern", name),
				Fixable: false,
			})
			continue
		}

		validateRecordsFile(path, sourceName, projectID, fileDateRange, result)
	}
	reportPeriodOverlaps(dir, entries, result)
}

// lineValidator parses a single JSONL line and returns:
//   - date: the record's date string (for date-mismatch checking)
//   - key: a dedup key (for duplicate detection)
//   - issues: any field-level validation issues found
//
// Returning an empty date signals a parse failure (the caller handles malformed JSON).
type lineValidator func(line []byte, path string, lineNum int) (date string, key string, issues []Issue)

func validateJSONLFile(path, fileDateRange string, result *ValidationResult, validate lineValidator) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	result.FilesRead++

	scanner := newLineReader(f)

	seen := make(map[string]int)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		result.LinesRead++

		date, key, issues := validate(scanner.Bytes(), path, lineNum)
		result.Issues = append(result.Issues, issues...)
		if date == "" && key == "" {
			continue
		}

		result.RecordCount++

		if date != "" {
			if !dateMatchesFile(date, fileDateRange) {
				result.Issues = append(result.Issues, Issue{
					Type:    IssueDateMismatch,
					Path:    path,
					Line:    lineNum,
					Message: fmt.Sprintf("line %d: date %s does not belong in file %s", lineNum, date, filepath.Base(path)),
					Fixable: true,
				})
			}
		}

		if key != "" {
			if firstLine, exists := seen[key]; exists {
				result.Issues = append(result.Issues, Issue{
					Type:    IssueDuplicate,
					Path:    path,
					Line:    lineNum,
					Message: fmt.Sprintf("line %d: duplicate of line %d", lineNum, firstLine),
					Fixable: true,
				})
			} else {
				seen[key] = lineNum
			}
		}
	}
}

func validateRecordsFile(path, sourceName, projectID, fileDateRange string, result *ValidationResult) {
	validateJSONLFile(path, fileDateRange, result, func(line []byte, p string, lineNum int) (string, string, []Issue) {
		var r source.Record
		if err := json.Unmarshal(line, &r); err != nil {
			return "", "", []Issue{{
				Type: IssueMalformedJSON, Path: p, Line: lineNum,
				Message: fmt.Sprintf("line %d: %v", lineNum, err), Fixable: true,
			}}
		}

		var issues []Issue
		if r.Metric == "" {
			issues = append(issues, Issue{
				Type: IssueEmptyField, Path: p, Line: lineNum,
				Message: fmt.Sprintf("line %d: empty metric field", lineNum),
			})
		}
		if r.ProjectID == "" {
			issues = append(issues, Issue{
				Type: IssueEmptyField, Path: p, Line: lineNum,
				Message: fmt.Sprintf("line %d: empty project_id field", lineNum),
			})
		}

		if sourceName == "openvsx" && (r.Metric == "rating" || r.Metric == "reviews") {
			issues = append(issues, Issue{
				Type: IssueDeprecatedMetric, Path: p, Line: lineNum,
				Message: fmt.Sprintf("line %d: deprecated metric %q (openvsx)", lineNum, r.Metric),
				Fixable: true,
			})
		}

		date := r.Date
		if !datePattern.MatchString(date) {
			issues = append(issues, Issue{
				Type: IssueInvalidDate, Path: p, Line: lineNum,
				Message: fmt.Sprintf("line %d: invalid date %q", lineNum, date),
			})
			date = ""
		} else if _, err := dateutil.ParseDate(date); err != nil {
			issues = append(issues, Issue{
				Type: IssueInvalidDate, Path: p, Line: lineNum,
				Message: fmt.Sprintf("line %d: unparseable date %q", lineNum, date),
			})
			date = ""
		}

		return date, dedupKey(r), issues
	})
}

func validateEventsDir(dir string, result *ValidationResult) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		path := filepath.Join(dir, name)

		if isLeftoverTempFile(name) {
			result.Issues = append(result.Issues, leftoverTempFileIssue(path))
			continue
		}

		fileDateRange := extractDateRange(name)
		if fileDateRange == "" {
			result.Issues = append(result.Issues, Issue{
				Type:    IssueUnexpectedFile,
				Path:    path,
				Message: fmt.Sprintf("file %q does not match expected naming pattern", name),
				Fixable: false,
			})
			continue
		}

		validateEventsFile(path, fileDateRange, result)
	}
	reportPeriodOverlaps(dir, entries, result)
}

// isLeftoverTempFile reports whether name is a temp file left by an
// interrupted atomic write: ".<target>.<random>.tmp", or ".tmp-<random>.jsonl"
// from versions before that naming.
func isLeftoverTempFile(name string) bool {
	if !strings.HasPrefix(name, ".") {
		return false
	}
	return strings.HasSuffix(name, ".tmp") || (strings.HasPrefix(name, ".tmp-") && strings.HasSuffix(name, ".jsonl"))
}

func leftoverTempFileIssue(path string) Issue {
	return Issue{
		Type:    IssueLeftoverTempFile,
		Path:    path,
		Message: "temp file left by an interrupted write",
		Fixable: true,
	}
}

// reportPeriodOverlaps flags monthly files that still have daily files next
// to them, and yearly files that still have monthly files. Every query counts
// such data twice until it is merged.
func reportPeriodOverlaps(dir string, entries []os.DirEntry, result *ValidationResult) {
	periods := make(map[string]bool)
	for _, e := range entries {
		if m := monthlyPattern.FindStringSubmatch(e.Name()); m != nil {
			periods[m[1]] = true
		} else if m := yearlyPattern.FindStringSubmatch(e.Name()); m != nil {
			periods[m[1]] = true
		}
	}

	inner := make(map[string]int)
	for _, e := range entries {
		if group, ok := groupDailyFile(e.Name()); ok && periods[group] {
			inner[group]++
		} else if group, ok := groupMonthlyFile(e.Name()); ok && periods[group] {
			inner[group]++
		}
	}

	groups := make([]string, 0, len(inner))
	for group := range inner {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	for _, group := range groups {
		kind := "daily"
		if len(group) == len("2006") {
			kind = "monthly"
		}
		result.Issues = append(result.Issues, Issue{
			Type:    IssuePeriodOverlap,
			Path:    filepath.Join(dir, group+".jsonl"),
			Message: fmt.Sprintf("%d %s file(s) hold data that also belongs in %s.jsonl", inner[group], kind, group),
			Fixable: true,
		})
	}
}

func validateEventsFile(path, fileDateRange string, result *ValidationResult) {
	validateJSONLFile(path, fileDateRange, result, func(line []byte, p string, lineNum int) (string, string, []Issue) {
		var e source.Event
		if err := json.Unmarshal(line, &e); err != nil {
			return "", "", []Issue{{
				Type: IssueMalformedJSON, Path: p, Line: lineNum,
				Message: fmt.Sprintf("line %d: %v", lineNum, err), Fixable: true,
			}}
		}

		var issues []Issue
		if e.Type == "" {
			issues = append(issues, Issue{
				Type: IssueEmptyField, Path: p, Line: lineNum,
				Message: fmt.Sprintf("line %d: empty type field", lineNum),
			})
		}

		if len(e.Datetime) < 10 {
			issues = append(issues, Issue{
				Type: IssueInvalidDatetime, Path: p, Line: lineNum,
				Message: fmt.Sprintf("line %d: datetime too short: %q", lineNum, e.Datetime),
			})
			return "", "", issues
		}

		if _, err := time.Parse(time.RFC3339, e.Datetime); err != nil {
			issues = append(issues, Issue{
				Type: IssueInvalidDatetime, Path: p, Line: lineNum,
				Message: fmt.Sprintf("line %d: invalid datetime %q", lineNum, e.Datetime),
			})
			return "", dedupEventKey(e), issues
		}

		return e.Datetime[:10], dedupEventKey(e), issues
	})
}

func extractDateRange(filename string) string {
	if m := dailyPattern.FindStringSubmatch(filename); m != nil {
		return m[1]
	}
	if m := monthlyPattern.FindStringSubmatch(filename); m != nil {
		return m[1]
	}
	if m := yearlyPattern.FindStringSubmatch(filename); m != nil {
		return m[1]
	}
	return ""
}

func dateMatchesFile(recordDate, fileDateRange string) bool {
	switch len(fileDateRange) {
	case 10: // daily: 2025-06-01
		return recordDate == fileDateRange
	case 7: // monthly: 2025-06
		return strings.HasPrefix(recordDate, fileDateRange)
	case 4: // yearly: 2025
		return strings.HasPrefix(recordDate, fileDateRange)
	}
	return false
}

type FixResult struct {
	Fixed   int
	Skipped int
	Errors  []error
}

func FixMalformedJSON(paths map[string][]int) *FixResult {
	result := &FixResult{}
	for path, badLines := range paths {
		if err := removeLinesFromFile(path, badLines); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("%s: %w", path, err))
		} else {
			result.Fixed += len(badLines)
		}
	}
	return result
}

func FixDuplicates(paths []string) *FixResult {
	result := &FixResult{}
	for _, path := range paths {
		var n int
		var err error
		switch dataCategory(path) {
		case EventsDir:
			n, err = deduplicateEventsFile(path)
		case MetricsDir:
			n, err = deduplicateRecordsFile(path)
		case ContentDataDir:
			n, err = deduplicateContentFile(path)
		default:
			err = fmt.Errorf("not a metrics, events or content file")
		}
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("%s: %w", path, err))
		} else {
			result.Fixed += n
		}
	}
	return result
}

func FixDateMismatches(paths []string) *FixResult {
	result := &FixResult{}
	for _, path := range paths {
		var n int
		var err error
		switch dataCategory(path) {
		case EventsDir:
			n, err = regroupEventsFile(path)
		case MetricsDir:
			n, err = regroupRecordsFile(path)
		default:
			err = fmt.Errorf("not a metrics or events file")
		}
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("%s: %w", path, err))
		} else {
			result.Fixed += n
		}
	}
	return result
}

// FixPeriodOverlaps merges daily files into the monthly file next to them and
// monthly files into the yearly file next to them, the same way compaction
// merges late data. paths are the period files reported by ValidateData.
func FixPeriodOverlaps(paths []string) *FixResult {
	result := &FixResult{}
	dirs := make(map[string]string)
	for _, path := range paths {
		dirs[filepath.Dir(path)] = dataCategory(path)
	}
	for dir, category := range dirs {
		before, err := os.ReadDir(dir)
		if err != nil {
			result.Errors = append(result.Errors, err)
			continue
		}
		switch category {
		case MetricsDir:
			err = mergeOverlappingPeriods(dir, recordAggregateSpec())
		case EventsDir:
			err = mergeOverlappingPeriods(dir, eventAggregateSpec())
		default:
			err = fmt.Errorf("not a metrics or events directory")
		}
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("%s: %w", dir, err))
			continue
		}
		after, err := os.ReadDir(dir)
		if err != nil {
			result.Errors = append(result.Errors, err)
			continue
		}
		result.Fixed += len(before) - len(after)
	}
	return result
}

// mergeOverlappingPeriods folds daily files into an existing monthly file and
// monthly files into an existing yearly file.
func mergeOverlappingPeriods[T any](dir string, spec aggregateSpec[T]) error {
	periodExists := func(group string, _ time.Time) bool {
		_, err := os.Stat(filepath.Join(dir, group+".jsonl"))
		return err == nil
	}
	if err := aggregateFiles(dir, time.Time{}, spec, groupDailyFile, periodExists, "month", "daily into monthly"); err != nil {
		return err
	}
	return aggregateFiles(dir, time.Time{}, spec, groupMonthlyFile, periodExists, "year", "monthly into yearly")
}

// FixLeftoverTempFiles removes temp files left by interrupted writes.
func FixLeftoverTempFiles(paths []string) *FixResult {
	result := &FixResult{}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			result.Errors = append(result.Errors, err)
		} else {
			result.Fixed++
		}
	}
	return result
}

// dataCategory returns the category a data file belongs to (metrics, events or
// content), judging by its position: <data>/<category>/<source>/<project>/<file>.
// It doesn't depend on what the data directory is called.
func dataCategory(path string) string {
	return filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(path))))
}

func FixOrphanDirs(paths []string) *FixResult {
	result := &FixResult{}
	for _, path := range paths {
		if err := os.RemoveAll(path); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("%s: %w", path, err))
		} else {
			result.Fixed++
		}
	}
	return result
}

func FixSourceMismatches(paths []string) *FixResult {
	result := &FixResult{}
	for _, path := range paths {
		n, err := fixSourceInFile(path)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("%s: %w", path, err))
		} else {
			result.Fixed += n
		}
	}
	return result
}

func fixSourceInFile(path string) (int, error) {
	sourceName := filepath.Base(filepath.Dir(filepath.Dir(path)))

	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	scanner := newLineReader(f)

	var lines [][]byte
	fixed := 0

	for scanner.Scan() {
		line := scanner.Bytes()
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			cp := make([]byte, len(line))
			copy(cp, line)
			lines = append(lines, cp)
			continue
		}

		var src string
		if s, ok := raw["source"]; ok {
			_ = json.Unmarshal(s, &src)
		}

		if src != "" && src != sourceName {
			raw["source"], _ = json.Marshal(sourceName)
			rewritten, err := json.Marshal(raw)
			if err != nil {
				cp := make([]byte, len(line))
				copy(cp, line)
				lines = append(lines, cp)
				continue
			}
			lines = append(lines, rewritten)
			fixed++
		} else {
			cp := make([]byte, len(line))
			copy(cp, line)
			lines = append(lines, cp)
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	_ = f.Close()

	if fixed == 0 {
		return 0, nil
	}

	return fixed, writeLines(path, lines)
}

// FixProjectMismatches backfills or corrects the project_id field in content
// JSONL files, deriving the correct value from the parent directory name
// (data/content/<source>/<project-id>/<file>.jsonl).
func FixProjectMismatches(paths []string) *FixResult {
	result := &FixResult{}
	for _, path := range paths {
		n, err := fixProjectIDInFile(path)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("%s: %w", path, err))
		} else {
			result.Fixed += n
		}
	}
	return result
}

func fixProjectIDInFile(path string) (int, error) {
	projectID := filepath.Base(filepath.Dir(path))

	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	scanner := newLineReader(f)

	var lines [][]byte
	fixed := 0

	for scanner.Scan() {
		line := scanner.Bytes()
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			cp := make([]byte, len(line))
			copy(cp, line)
			lines = append(lines, cp)
			continue
		}

		var pid string
		if p, ok := raw["project_id"]; ok {
			_ = json.Unmarshal(p, &pid)
		}

		if pid != projectID {
			raw["project_id"], _ = json.Marshal(projectID)
			rewritten, err := json.Marshal(raw)
			if err != nil {
				cp := make([]byte, len(line))
				copy(cp, line)
				lines = append(lines, cp)
				continue
			}
			lines = append(lines, rewritten)
			fixed++
		} else {
			cp := make([]byte, len(line))
			copy(cp, line)
			lines = append(lines, cp)
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	_ = f.Close()

	if fixed == 0 {
		return 0, nil
	}

	return fixed, writeLines(path, lines)
}

func FixDeprecatedMetrics(paths []string) *FixResult {
	result := &FixResult{}
	for _, path := range paths {
		n, err := fixDeprecatedMetricsInFile(path)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("%s: %w", path, err))
		} else {
			result.Fixed += n
		}
	}
	return result
}

var deprecatedOpenVSXMetrics = map[string]string{
	"rating":  "total_ratings",
	"reviews": "total_reviews",
}

func fixDeprecatedMetricsInFile(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	scanner := newLineReader(f)

	var lines [][]byte
	fixed := 0

	for scanner.Scan() {
		line := scanner.Bytes()
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			cp := make([]byte, len(line))
			copy(cp, line)
			lines = append(lines, cp)
			continue
		}

		var metric string
		if m, ok := raw["metric"]; ok {
			_ = json.Unmarshal(m, &metric)
		}

		newMetric, isDeprecated := deprecatedOpenVSXMetrics[metric]
		if !isDeprecated {
			cp := make([]byte, len(line))
			copy(cp, line)
			lines = append(lines, cp)
			continue
		}

		raw["metric"], _ = json.Marshal(newMetric)

		if metric == "rating" {
			if v, ok := raw["value"]; ok {
				normalized, err := normalizeDeprecatedOpenVSXRating(v)
				if err != nil {
					return 0, err
				}
				raw["value"] = normalized
			}
		}

		rewritten, err := json.Marshal(raw)
		if err != nil {
			cp := make([]byte, len(line))
			copy(cp, line)
			lines = append(lines, cp)
			continue
		}
		lines = append(lines, rewritten)
		fixed++
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	_ = f.Close()

	if fixed == 0 {
		return 0, nil
	}

	return fixed, writeLines(path, lines)
}

func normalizeDeprecatedOpenVSXRating(value json.RawMessage) (json.RawMessage, error) {
	var val float64
	if err := json.Unmarshal(value, &val); err != nil {
		return nil, fmt.Errorf("decode rating value: %w", err)
	}
	if val < 0 || val > 5 {
		return value, nil
	}
	normalized, err := json.Marshal(int64(math.Round(val * 100)))
	if err != nil {
		return nil, fmt.Errorf("encode rating value: %w", err)
	}
	return normalized, nil
}

func removeLinesFromFile(path string, badLines []int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	bad := make(map[int]bool, len(badLines))
	for _, l := range badLines {
		bad[l] = true
	}

	var kept [][]byte
	scanner := newLineReader(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if !bad[lineNum] {
			line := make([]byte, len(scanner.Bytes()))
			copy(line, scanner.Bytes())
			kept = append(kept, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	_ = f.Close()

	if len(kept) == 0 {
		return os.Remove(path)
	}

	return writeLines(path, kept)
}

func deduplicateRecordsFile(path string) (int, error) {
	records, err := ReadRecords(path)
	if err != nil {
		return 0, err
	}

	deduped := dedup(records)
	removed := len(records) - len(deduped)
	if removed == 0 {
		return 0, nil
	}

	if err := writeFileAtomic(path, deduped); err != nil {
		return 0, err
	}
	return removed, nil
}

// deduplicateContentFile removes repeated content entries, keyed like the
// upsert in WriteContent by (target, id): the last occurrence wins, at the
// position of the first. Lines are kept verbatim, including ones that don't
// parse; those are reported and fixed as malformed JSON.
func deduplicateContentFile(path string) (int, error) {
	lines, err := readFileLines(path)
	if err != nil {
		return 0, err
	}

	index := make(map[contentKey]int)
	kept := make([][]byte, 0, len(lines))
	for _, line := range lines {
		var e source.ContentEntry
		if err := json.Unmarshal(line, &e); err != nil || e.ID == "" {
			kept = append(kept, line)
			continue
		}
		k := contentKey{target: e.Target, id: e.ID}
		if i, ok := index[k]; ok {
			kept[i] = line
			continue
		}
		index[k] = len(kept)
		kept = append(kept, line)
	}

	removed := len(lines) - len(kept)
	if removed == 0 {
		return 0, nil
	}
	if err := writeLines(path, kept); err != nil {
		return 0, err
	}
	return removed, nil
}

func deduplicateEventsFile(path string) (int, error) {
	events, err := ReadEvents(path)
	if err != nil {
		return 0, err
	}

	deduped := dedupEvents(events)
	removed := len(events) - len(deduped)
	if removed == 0 {
		return 0, nil
	}

	if err := writeEventsFileAtomic(path, deduped); err != nil {
		return 0, err
	}
	return removed, nil
}

func regroupRecordsFile(path string) (int, error) {
	filename := filepath.Base(path)
	fileDateRange := extractDateRange(filename)
	if fileDateRange == "" {
		return 0, fmt.Errorf("cannot determine date range from filename %s", filename)
	}

	records, err := ReadRecords(path)
	if err != nil {
		return 0, err
	}

	var belong []source.Record
	misplaced := make(map[string][]source.Record)

	for _, r := range records {
		if dateMatchesFile(r.Date, fileDateRange) {
			belong = append(belong, r)
		} else {
			misplaced[r.Date] = append(misplaced[r.Date], r)
		}
	}

	if len(misplaced) == 0 {
		return 0, nil
	}

	moved := 0
	dir := filepath.Dir(path)

	for date, recs := range misplaced {
		targetPath := filepath.Join(dir, date+".jsonl")
		existing, _ := ReadRecords(targetPath)
		combined := append(existing, recs...)
		combined = dedup(combined)
		sort.Slice(combined, func(i, j int) bool {
			return combined[i].Date < combined[j].Date
		})
		if err := writeFileAtomic(targetPath, combined); err != nil {
			return moved, err
		}
		moved += len(recs)
	}

	if len(belong) == 0 {
		if err := os.Remove(path); err != nil {
			return moved, err
		}
	} else {
		if err := writeFileAtomic(path, belong); err != nil {
			return moved, err
		}
	}

	return moved, nil
}

func regroupEventsFile(path string) (int, error) {
	filename := filepath.Base(path)
	fileDateRange := extractDateRange(filename)
	if fileDateRange == "" {
		return 0, fmt.Errorf("cannot determine date range from filename %s", filename)
	}

	events, err := ReadEvents(path)
	if err != nil {
		return 0, err
	}

	var belong []source.Event
	misplaced := make(map[string][]source.Event)

	for _, e := range events {
		if len(e.Datetime) < 10 {
			belong = append(belong, e)
			continue
		}
		eventDate := e.Datetime[:10]
		if dateMatchesFile(eventDate, fileDateRange) {
			belong = append(belong, e)
		} else {
			misplaced[eventDate] = append(misplaced[eventDate], e)
		}
	}

	if len(misplaced) == 0 {
		return 0, nil
	}

	moved := 0
	dir := filepath.Dir(path)

	for date, evts := range misplaced {
		targetPath := filepath.Join(dir, date+".jsonl")
		existing, _ := ReadEvents(targetPath)
		combined := append(existing, evts...)
		combined = dedupEvents(combined)
		sort.Slice(combined, func(i, j int) bool {
			return combined[i].Datetime < combined[j].Datetime
		})
		if err := writeEventsFileAtomic(targetPath, combined); err != nil {
			return moved, err
		}
		moved += len(evts)
	}

	if len(belong) == 0 {
		if err := os.Remove(path); err != nil {
			return moved, err
		}
	} else {
		if err := writeEventsFileAtomic(path, belong); err != nil {
			return moved, err
		}
	}

	return moved, nil
}

func validateContentCategory(categoryDir string, projectIDs map[string]bool, result *ValidationResult) {
	sourceDirs, err := os.ReadDir(categoryDir)
	if err != nil {
		return
	}

	for _, sourceEntry := range sourceDirs {
		if !sourceEntry.IsDir() {
			continue
		}
		sourceName := sourceEntry.Name()
		sourcePath := filepath.Join(categoryDir, sourceName)

		projDirs, err := os.ReadDir(sourcePath)
		if err != nil {
			continue
		}

		for _, projEntry := range projDirs {
			if !projEntry.IsDir() {
				continue
			}
			projID := projEntry.Name()
			projPath := filepath.Join(sourcePath, projID)

			if projectIDs != nil && !projectIDs[projID] {
				result.Issues = append(result.Issues, Issue{
					Type:    IssueOrphanDir,
					Path:    projPath,
					Message: fmt.Sprintf("directory for unknown project %q (source: %s)", projID, sourceName),
					Fixable: true,
				})
				continue
			}

			validateContentDir(projPath, sourceName, projID, result)
		}
	}
}

func validateContentDir(dir, sourceName, projectID string, result *ValidationResult) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		path := filepath.Join(dir, name)

		if isLeftoverTempFile(name) {
			result.Issues = append(result.Issues, leftoverTempFileIssue(path))
			continue
		}

		if !strings.HasSuffix(name, ".jsonl") {
			result.Issues = append(result.Issues, Issue{
				Type:    IssueUnexpectedFile,
				Path:    path,
				Message: fmt.Sprintf("file %q is not a .jsonl file", name),
				Fixable: false,
			})
			continue
		}

		validateContentFile(path, sourceName, projectID, result)
	}
}

func validateContentFile(path, sourceName, projectID string, result *ValidationResult) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	result.FilesRead++

	scanner := newLineReader(f)

	seen := make(map[contentKey]int)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		result.LinesRead++

		var e source.ContentEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			result.Issues = append(result.Issues, Issue{
				Type:    IssueMalformedJSON,
				Path:    path,
				Line:    lineNum,
				Message: fmt.Sprintf("line %d: %v", lineNum, err),
				Fixable: true,
			})
			continue
		}

		result.RecordCount++

		if e.ID == "" {
			result.Issues = append(result.Issues, Issue{
				Type:    IssueEmptyField,
				Path:    path,
				Line:    lineNum,
				Message: fmt.Sprintf("line %d: empty id field", lineNum),
			})
		}
		if e.Title == "" {
			result.Issues = append(result.Issues, Issue{
				Type:    IssueEmptyField,
				Path:    path,
				Line:    lineNum,
				Message: fmt.Sprintf("line %d: empty title field", lineNum),
			})
		}
		if e.Source == "" {
			result.Issues = append(result.Issues, Issue{
				Type:    IssueEmptyField,
				Path:    path,
				Line:    lineNum,
				Message: fmt.Sprintf("line %d: empty source field", lineNum),
			})
		} else if e.Source != sourceName {
			result.Issues = append(result.Issues, Issue{
				Type:    IssueSourceMismatch,
				Path:    path,
				Line:    lineNum,
				Message: fmt.Sprintf("line %d: source %q does not match directory %q", lineNum, e.Source, sourceName),
				Fixable: true,
			})
		}
		if e.ProjectID == "" {
			result.Issues = append(result.Issues, Issue{
				Type:    IssueProjectMismatch,
				Path:    path,
				Line:    lineNum,
				Message: fmt.Sprintf("line %d: empty project_id field", lineNum),
				Fixable: true,
			})
		} else if e.ProjectID != projectID {
			result.Issues = append(result.Issues, Issue{
				Type:    IssueProjectMismatch,
				Path:    path,
				Line:    lineNum,
				Message: fmt.Sprintf("line %d: project_id %q does not match directory %q", lineNum, e.ProjectID, projectID),
				Fixable: true,
			})
		}
		if e.Target == "" {
			result.Issues = append(result.Issues, Issue{
				Type:    IssueEmptyField,
				Path:    path,
				Line:    lineNum,
				Message: fmt.Sprintf("line %d: empty target field", lineNum),
			})
		}
		// An entry must carry at least one timestamp. Some content (e.g. RSS
		// person/software feeds) has only updated_at and no published_at.
		if e.PublishedAt == "" && e.UpdatedAt == "" {
			result.Issues = append(result.Issues, Issue{
				Type:    IssueEmptyField,
				Path:    path,
				Line:    lineNum,
				Message: fmt.Sprintf("line %d: no published_at or updated_at field", lineNum),
			})
		}
		for _, ts := range []struct{ field, value string }{
			{"published_at", e.PublishedAt},
			{"updated_at", e.UpdatedAt},
		} {
			if ts.value == "" {
				continue
			}
			if _, err := time.Parse(time.RFC3339, ts.value); err != nil {
				if _, err2 := time.Parse("2006-01-02T15:04:05Z", ts.value); err2 != nil {
					result.Issues = append(result.Issues, Issue{
						Type:    IssueInvalidDatetime,
						Path:    path,
						Line:    lineNum,
						Message: fmt.Sprintf("line %d: invalid %s %q", lineNum, ts.field, ts.value),
					})
				}
			}
		}

		if e.ID != "" {
			// Same key as the upsert in WriteContent: entries from different
			// targets may share an ID.
			k := contentKey{target: e.Target, id: e.ID}
			if firstLine, exists := seen[k]; exists {
				result.Issues = append(result.Issues, Issue{
					Type:    IssueDuplicate,
					Path:    path,
					Line:    lineNum,
					Message: fmt.Sprintf("line %d: duplicate id %q for %s (first at line %d)", lineNum, e.ID, e.Target, firstLine),
					Fixable: true,
				})
			} else {
				seen[k] = lineNum
			}
		}
	}
}

// readFileLines returns the non-empty lines of a file without a line length
// limit.
func readFileLines(path string) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var lines [][]byte
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadBytes('\n')
		if trimmed := bytes.TrimRight(line, "\r\n"); len(trimmed) > 0 {
			lines = append(lines, trimmed)
		}
		if err == io.EOF {
			return lines, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func writeLines(path string, lines [][]byte) error {
	return writeJSONLLinesAtomic(path, lines)
}
