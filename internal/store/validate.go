package store

import (
	"bufio"
	"encoding/json"
	"fmt"
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
		if name == WatermarkFileName {
			continue
		}
		path := filepath.Join(dir, name)

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

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

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
		if isEventPath(path) {
			n, err := deduplicateEventsFile(path)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("%s: %w", path, err))
			} else {
				result.Fixed += n
			}
		} else {
			n, err := deduplicateRecordsFile(path)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("%s: %w", path, err))
			} else {
				result.Fixed += n
			}
		}
	}
	return result
}

func FixDateMismatches(paths []string) *FixResult {
	result := &FixResult{}
	for _, path := range paths {
		if isEventPath(path) {
			n, err := regroupEventsFile(path)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("%s: %w", path, err))
			} else {
				result.Fixed += n
			}
		} else {
			n, err := regroupRecordsFile(path)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("%s: %w", path, err))
			} else {
				result.Fixed += n
			}
		}
	}
	return result
}

func isEventPath(path string) bool {
	normalized := filepath.ToSlash(path)
	return strings.Contains(normalized, "data/events/")
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

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

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

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

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

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

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
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
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

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	seen := make(map[string]int)
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
		if e.PublishedAt == "" {
			result.Issues = append(result.Issues, Issue{
				Type:    IssueEmptyField,
				Path:    path,
				Line:    lineNum,
				Message: fmt.Sprintf("line %d: empty published_at field", lineNum),
			})
		} else if _, err := time.Parse(time.RFC3339, e.PublishedAt); err != nil {
			if _, err2 := time.Parse("2006-01-02T15:04:05Z", e.PublishedAt); err2 != nil {
				result.Issues = append(result.Issues, Issue{
					Type:    IssueInvalidDatetime,
					Path:    path,
					Line:    lineNum,
					Message: fmt.Sprintf("line %d: invalid published_at %q", lineNum, e.PublishedAt),
				})
			}
		}

		if e.ID != "" {
			if firstLine, exists := seen[e.ID]; exists {
				result.Issues = append(result.Issues, Issue{
					Type:    IssueDuplicate,
					Path:    path,
					Line:    lineNum,
					Message: fmt.Sprintf("line %d: duplicate id %q (first at line %d)", lineNum, e.ID, firstLine),
					Fixable: true,
				})
			} else {
				seen[e.ID] = lineNum
			}
		}
	}
}

func writeLines(path string, lines [][]byte) error {
	return writeJSONLLinesAtomic(path, lines)
}
