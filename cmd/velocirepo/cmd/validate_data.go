package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/posit-dev/velocirepo/internal/store"
	"github.com/posit-dev/velocirepo/internal/ui"
	"github.com/spf13/cobra"
)

func validateDataCmd() *cobra.Command {
	var fix bool

	cmd := &cobra.Command{
		Use:     "validate-data",
		Short:   "Check data files for integrity issues",
		GroupID: "data",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			dataDir := cfg.DataDir()

			var projectIDs map[string]bool
			if len(cfg.Projects) > 0 {
				projectIDs = make(map[string]bool, len(cfg.Projects))
				for id := range cfg.Projects {
					projectIDs[id] = true
				}
			}

			ui.Infof("scanning %s", dataDir)

			result, err := store.ValidateData(dataDir, projectIDs)
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintf(out, "Scanned %d files, %d lines, %d records\n\n",
				result.FilesRead, result.LinesRead, result.RecordCount)

			if len(result.Issues) == 0 {
				_, _ = fmt.Fprintln(out, "No issues found.")
				return nil
			}

			grouped := groupIssuesByType(result.Issues)
			types := sortedIssueTypes(grouped)

			for _, t := range types {
				issues := grouped[t]
				_, _ = fmt.Fprintf(out, "%s (%d)\n", t, len(issues))
				for _, issue := range issues {
					marker := "  ✗"
					if issue.Fixable {
						marker = "  ⚠"
					}
					_, _ = fmt.Fprintf(out, "%s %s: %s\n", marker, relativePath(dataDir, issue.Path), issue.Message)
				}
				_, _ = fmt.Fprintln(out)
			}

			fixable := countFixable(result.Issues)
			_, _ = fmt.Fprintf(out, "%d issue(s), %d fixable\n", len(result.Issues), fixable)

			if fixable == 0 || !fix {
				if fixable > 0 && !fix {
					_, _ = fmt.Fprintln(out, "\nRun with --fix to apply fixes interactively.")
				}
				if len(result.Issues) > 0 {
					return fmt.Errorf("%d issue(s) found", len(result.Issues))
				}
				return nil
			}

			_, _ = fmt.Fprintln(out)

			if !isInteractive() {
				return fmt.Errorf("--fix requires an interactive terminal")
			}

			reader := bufio.NewReader(os.Stdin)
			return runDataFixHandlers(out, reader, grouped)
		},
	}

	cmd.Flags().BoolVar(&fix, "fix", false, "interactively fix issues")

	return cmd
}

func groupIssuesByType(issues []store.Issue) map[store.IssueType][]store.Issue {
	grouped := make(map[store.IssueType][]store.Issue)
	for _, issue := range issues {
		grouped[issue.Type] = append(grouped[issue.Type], issue)
	}
	return grouped
}

func sortedIssueTypes(grouped map[store.IssueType][]store.Issue) []store.IssueType {
	types := make([]store.IssueType, 0, len(grouped))
	for t := range grouped {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool {
		return types[i] < types[j]
	})
	return types
}

func countFixable(issues []store.Issue) int {
	count := 0
	for _, issue := range issues {
		if issue.Fixable {
			count++
		}
	}
	return count
}

type dataFixHandler struct {
	issueType store.IssueType
	prepare   func([]store.Issue) *dataFixAction
}

type dataFixAction struct {
	confirmMessage string
	successFormat  string
	fix            func() *store.FixResult
}

var dataFixHandlers = []dataFixHandler{
	{
		issueType: store.IssueMalformedJSON,
		prepare: func(issues []store.Issue) *dataFixAction {
			paths := collectMalformedPaths(issues)
			if len(paths) == 0 {
				return nil
			}
			lineCount := 0
			for _, lines := range paths {
				lineCount += len(lines)
			}
			return &dataFixAction{
				confirmMessage: fmt.Sprintf("Remove %d malformed JSON line(s) from %d file(s)?", lineCount, len(paths)),
				successFormat:  "  Removed %d line(s)\n",
				fix: func() *store.FixResult {
					return store.FixMalformedJSON(paths)
				},
			}
		},
	},
	{
		issueType: store.IssueDuplicate,
		prepare: func(issues []store.Issue) *dataFixAction {
			paths := collectUniquePaths(issues)
			if len(paths) == 0 {
				return nil
			}
			return &dataFixAction{
				confirmMessage: fmt.Sprintf("Deduplicate %d record(s) across %d file(s)?", len(issues), len(paths)),
				successFormat:  "  Removed %d duplicate(s)\n",
				fix: func() *store.FixResult {
					return store.FixDuplicates(paths)
				},
			}
		},
	},
	{
		issueType: store.IssueDateMismatch,
		prepare: func(issues []store.Issue) *dataFixAction {
			paths := collectUniquePaths(issues)
			if len(paths) == 0 {
				return nil
			}
			return &dataFixAction{
				confirmMessage: fmt.Sprintf("Move %d misplaced record(s) to correct files across %d file(s)?", len(issues), len(paths)),
				successFormat:  "  Moved %d record(s)\n",
				fix: func() *store.FixResult {
					return store.FixDateMismatches(paths)
				},
			}
		},
	},
	{
		issueType: store.IssueSourceMismatch,
		prepare: func(issues []store.Issue) *dataFixAction {
			paths := collectUniquePaths(issues)
			if len(paths) == 0 {
				return nil
			}
			return &dataFixAction{
				confirmMessage: fmt.Sprintf("Fix source field in %d record(s) across %d file(s)?", len(issues), len(paths)),
				successFormat:  "  Fixed %d record(s)\n",
				fix: func() *store.FixResult {
					return store.FixSourceMismatches(paths)
				},
			}
		},
	},
	{
		issueType: store.IssueProjectMismatch,
		prepare: func(issues []store.Issue) *dataFixAction {
			paths := collectUniquePaths(issues)
			if len(paths) == 0 {
				return nil
			}
			return &dataFixAction{
				confirmMessage: fmt.Sprintf("Fix project_id field in %d record(s) across %d file(s)?", len(issues), len(paths)),
				successFormat:  "  Fixed %d record(s)\n",
				fix: func() *store.FixResult {
					return store.FixProjectMismatches(paths)
				},
			}
		},
	},
	{
		issueType: store.IssueOrphanDir,
		prepare: func(issues []store.Issue) *dataFixAction {
			paths := collectOrphanPaths(issues)
			if len(paths) == 0 {
				return nil
			}
			return &dataFixAction{
				confirmMessage: fmt.Sprintf("Remove %d orphan director(ies) not in config?", len(paths)),
				successFormat:  "  Removed %d director(ies)\n",
				fix: func() *store.FixResult {
					return store.FixOrphanDirs(paths)
				},
			}
		},
	},
	{
		issueType: store.IssueDeprecatedMetric,
		prepare: func(issues []store.Issue) *dataFixAction {
			paths := collectUniquePaths(issues)
			if len(paths) == 0 {
				return nil
			}
			return &dataFixAction{
				confirmMessage: fmt.Sprintf("Fix %d deprecated metric name(s) across %d file(s)?", len(issues), len(paths)),
				successFormat:  "  Fixed %d record(s)\n",
				fix: func() *store.FixResult {
					return store.FixDeprecatedMetrics(paths)
				},
			}
		},
	},
}

func runDataFixHandlers(out io.Writer, reader *bufio.Reader, grouped map[store.IssueType][]store.Issue) error {
	for _, handler := range dataFixHandlers {
		action := handler.prepare(grouped[handler.issueType])
		if action == nil {
			continue
		}
		ok, err := confirm(out, reader, action.confirmMessage)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		printDataFixResult(out, action.fix(), action.successFormat)
	}
	return nil
}

func printDataFixResult(out io.Writer, result *store.FixResult, successFormat string) {
	_, _ = fmt.Fprintf(out, successFormat, result.Fixed)
	for _, e := range result.Errors {
		ui.Errorf("  %v", e)
	}
}

func relativePath(base, path string) string {
	if len(path) > len(base) && path[:len(base)] == base {
		return path[len(base)+1:]
	}
	return path
}

func collectMalformedPaths(issues []store.Issue) map[string][]int {
	if len(issues) == 0 {
		return nil
	}
	paths := make(map[string][]int)
	for _, issue := range issues {
		paths[issue.Path] = append(paths[issue.Path], issue.Line)
	}
	return paths
}

func collectUniquePaths(issues []store.Issue) []string {
	if len(issues) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var paths []string
	for _, issue := range issues {
		if !seen[issue.Path] {
			seen[issue.Path] = true
			paths = append(paths, issue.Path)
		}
	}
	return paths
}

func collectOrphanPaths(issues []store.Issue) []string {
	if len(issues) == 0 {
		return nil
	}
	paths := make([]string, 0, len(issues))
	for _, issue := range issues {
		paths = append(paths, issue.Path)
	}
	return paths
}
