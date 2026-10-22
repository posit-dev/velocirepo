package check

import (
	"fmt"
	"strings"
)

// Summary renders a report as Markdown, for the job summary and the
// tracking issue.
func Summary(r Report) string {
	var b strings.Builder
	counts := map[Severity]int{}
	accepted := 0
	for _, f := range r.Findings {
		if f.Accepted {
			accepted++
			continue
		}
		counts[f.Severity]++
	}
	if len(r.Findings) == accepted {
		b.WriteString("No problems found.\n")
	} else {
		fmt.Fprintf(&b, "%d errors, %d warnings, %d notes.\n", counts[Error], counts[Warning], counts[Info])
	}
	if len(r.Findings) == 0 {
		return b.String()
	}

	b.WriteString("\n| | Check | Where | What | ID |\n|---|---|---|---|---|\n")
	for _, f := range r.Findings {
		mark := map[Severity]string{Error: "❌", Warning: "⚠️", Info: "ℹ️"}[f.Severity]
		if f.Accepted {
			mark = "✔️"
		}
		where := strings.Trim(strings.Join([]string{f.Source, f.Project, f.Target}, "/"), "/")
		what := f.Message
		if f.Accepted {
			what += " (accepted: " + f.Reason + ")"
		}
		fmt.Fprintf(&b, "| %s | `%s` | %s | %s | `%s` |\n", mark, f.Check, escape(where), escape(what), f.ID)
	}
	return b.String()
}

func escape(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s)
}
