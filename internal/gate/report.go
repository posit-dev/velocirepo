package gate

import (
	"fmt"
	"os"
	"strings"

	"github.com/posit-dev/velocirepo/internal/check"
)

// report writes the job summary and an annotation for every new finding.
func (r *run) report(out Outcome) {
	for _, f := range out.Changes.New {
		level := "warning"
		if f.Severity == check.Error {
			level = "error"
		} else if f.Severity == check.Info {
			level = "notice"
		}
		r.logf("::%s title=%s::%s %s", level, f.Check, strings.Trim(f.Source+"/"+f.Project, "/"), annotationEscape(f.Message))
	}
	if r.opts.SummaryPath == "" {
		return
	}
	var b strings.Builder
	b.WriteString("## velocirepo gate\n\n")
	var findings []check.Finding
	for _, f := range out.Changes.Open {
		findings = append(findings, f.Finding)
	}
	b.WriteString(check.Summary(check.Report{Findings: findings}))
	fmt.Fprintf(&b, "\n%d new, %d resolved since the last run.\n", len(out.Changes.New), len(out.Changes.Resolved))
	b.WriteString("\n### What enforcing would have done\n\n")
	b.WriteString("This version of the gate only reports: it committed everything. An enforcing gate would have:\n\n")
	list := func(what string, items []string) {
		if len(items) == 0 {
			fmt.Fprintf(&b, "- %s: nothing\n", what)
			return
		}
		fmt.Fprintf(&b, "- %s: %s\n", what, strings.Join(items, ", "))
	}
	list("restored removed records of", out.WouldRestore)
	list("kept the stored values of", out.WouldKeep)
	list("held back the new records of", out.WouldHoldBack)
	f, err := os.OpenFile(r.opts.SummaryPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		r.logf("::warning::job summary: %v", err)
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString(b.String())
}

// annotationEscape escapes a workflow command's message.
func annotationEscape(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}
