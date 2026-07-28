package ui

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
)

var colorEnabled = isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())
var quiet bool

const (
	reset  = "\033[0m"
	dim    = "\033[2m"
	bold   = "\033[1m"
	red    = "\033[31m"
	yellow = "\033[33m"
	green  = "\033[32m"
	cyan   = "\033[36m"
)

func SetQuiet(q bool) {
	quiet = q
}

func IsQuiet() bool {
	return quiet
}

func color(c, s string) string {
	if !colorEnabled {
		return s
	}
	return c + s + reset
}

func Prefix(source, project string) string {
	return fmt.Sprintf("[%s/%s]", project, source)
}

func Info(msg string) {
	if quiet {
		return
	}
	fmt.Fprintf(os.Stderr, "%s\n", color(dim, msg))
}

func Infof(format string, args ...interface{}) {
	Info(fmt.Sprintf(format, args...))
}

func Success(msg string) {
	if quiet {
		return
	}
	fmt.Fprintf(os.Stderr, "%s\n", color(green, msg))
}

func Successf(format string, args ...interface{}) {
	Success(fmt.Sprintf(format, args...))
}

func Warn(msg string) {
	if quiet {
		return
	}
	fmt.Fprintf(os.Stderr, "%s\n", color(yellow, "Warning: "+msg))
}

func Warnf(format string, args ...interface{}) {
	Warn(fmt.Sprintf(format, args...))
}

func Error(msg string) {
	fmt.Fprintf(os.Stderr, "%s\n", color(red, "Error: "+msg))
}

func Errorf(format string, args ...interface{}) {
	Error(fmt.Sprintf(format, args...))
}

func FetchPlan(sources int, projects int, tasks int) {
	if quiet {
		return
	}
	fmt.Fprintf(os.Stderr, "\n%s\n\n",
		color(bold, fmt.Sprintf("Fetching %d sources × %d projects (%d tasks)", sources, projects, tasks)))
}

func FetchProgress(completed, total int, symbol, source, project, detail string, clr string) {
	if quiet {
		return
	}
	counter := color(dim, fmt.Sprintf(" [%d/%d]", completed, total))
	label := fmt.Sprintf("%s/%s", project, source)
	fmt.Fprintf(os.Stderr, "%s %s %-28s %s\n", counter, color(clr, symbol), label, color(dim, detail))
}

func FetchStart(source, project, dateRange string) {
	if quiet {
		return
	}
	msg := fmt.Sprintf("%s fetching %s", Prefix(source, project), dateRange)
	fmt.Fprintf(os.Stderr, "%s\n", color(cyan, msg))
}

func FetchDone(source, project string, count int, duration time.Duration) {
	if quiet {
		return
	}
	msg := fmt.Sprintf("%s %d records in %s", Prefix(source, project), count, formatDuration(duration))
	fmt.Fprintf(os.Stderr, "%s\n", color(green, "  ✓ "+msg))
}

func FetchSkip(source, project, reason string) {
	if quiet {
		return
	}
	msg := fmt.Sprintf("%s skipped: %s", Prefix(source, project), reason)
	fmt.Fprintf(os.Stderr, "%s\n", color(dim, "  · "+msg))
}

func FetchError(source, project string, err error) {
	if quiet {
		return
	}
	msg := fmt.Sprintf("%s %v", Prefix(source, project), err)
	fmt.Fprintf(os.Stderr, "%s\n", color(red, "  ✗ "+msg))
}

func FetchWarn(source, project, msg string) {
	if quiet {
		return
	}
	fmt.Fprintf(os.Stderr, "%s\n", color(yellow, fmt.Sprintf("  ! %s %s", Prefix(source, project), msg)))
}

type FetchStats struct {
	Elapsed      time.Duration
	Records      int
	FilesWritten int
	APICalls     map[string]int
	Succeeded    int
	Skipped      int
	Failed       int
}

func FetchSummary(s FetchStats) {
	if quiet {
		return
	}

	fmt.Fprintf(os.Stderr, "\n%s\n", color(bold, fmt.Sprintf("Done in %s", formatDuration(s.Elapsed))))

	fmt.Fprintf(os.Stderr, "\n  %s %d lines across %d files\n",
		color(dim, "Records:"),
		s.Records, s.FilesWritten)

	if len(s.APICalls) > 0 {
		type hostCount struct {
			host  string
			count int
		}
		var hosts []hostCount
		for h, c := range s.APICalls {
			h = strings.TrimSuffix(h, ".io")
			h = strings.TrimSuffix(h, ".com")
			h = strings.TrimSuffix(h, ".org")
			h = strings.TrimPrefix(h, "api.")
			h = strings.TrimPrefix(h, "www.")
			hosts = append(hosts, hostCount{h, c})
		}
		sort.Slice(hosts, func(i, j int) bool { return hosts[i].count > hosts[j].count })

		var parts []string
		for _, hc := range hosts {
			parts = append(parts, fmt.Sprintf("%s ×%d", hc.host, hc.count))
		}
		fmt.Fprintf(os.Stderr, "  %s %s\n",
			color(dim, "API calls:"),
			strings.Join(parts, ", "))
	}

	fmt.Fprintf(os.Stderr, "  %s %d succeeded, %d skipped, %d failed\n\n",
		color(dim, "Results:"),
		s.Succeeded, s.Skipped, s.Failed)
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}
