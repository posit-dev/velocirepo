package gate

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/posit-dev/velocirepo/internal/fetch"
)

// dataMessage is the data commit's message: the run's date in UTC, the one
// date that's the same for every source, and in the body how far each
// source's data now goes, which can differ per source.
func dataMessage(runTime time.Time, freshness []fetch.TargetFreshness, heldBack []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Fetch metrics on %s\n", runTime.UTC().Format("2006-01-02"))

	// A source is stored through its least recent target.
	through := map[string]string{}
	for _, f := range freshness {
		if f.Series != "" || f.Watermark == "" {
			continue
		}
		if cur, ok := through[f.Source]; !ok || f.Watermark < cur {
			through[f.Source] = f.Watermark
		}
	}
	byDate := map[string][]string{}
	for src, date := range through {
		byDate[date] = append(byDate[date], src)
	}
	dates := make([]string, 0, len(byDate))
	for d := range byDate {
		dates = append(dates, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	if len(dates) > 0 {
		b.WriteString("\nStored through:\n")
		for _, d := range dates {
			sources := byDate[d]
			sort.Strings(sources)
			fmt.Fprintf(&b, "- %s: %s\n", strings.Join(sources, ", "), d)
		}
	}
	for _, h := range heldBack {
		fmt.Fprintf(&b, "\nHeld back: %s\n", h)
	}
	return b.String()
}

var (
	monthFile = regexp.MustCompile(`^(\d{4})-(\d{2})\.jsonl$`)
	yearFile  = regexp.MustCompile(`^(\d{4})\.jsonl$`)
)

// concatenationMessage names the months and years that files were
// concatenated into, such as "Concatenate October 2026", from the files the
// concatenation added.
func concatenationMessage(added []string) string {
	months := map[string]bool{}
	years := map[string]bool{}
	for _, f := range added {
		name := path.Base(f)
		if m := monthFile.FindStringSubmatch(name); m != nil {
			months[m[1]+"-"+m[2]] = true
		} else if m := yearFile.FindStringSubmatch(name); m != nil {
			years[m[1]] = true
		}
	}
	var parts []string
	sortedKeys := func(m map[string]bool) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	for _, ym := range sortedKeys(months) {
		t, _ := time.Parse("2006-01", ym)
		parts = append(parts, t.Format("January 2006"))
	}
	parts = append(parts, sortedKeys(years)...)
	if len(parts) == 0 {
		return "Concatenate data files\n"
	}
	return "Concatenate " + joinAnd(parts) + "\n"
}

func joinAnd(parts []string) string {
	switch len(parts) {
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}
