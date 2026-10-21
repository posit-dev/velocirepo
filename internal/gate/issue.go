package gate

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/posit-dev/velocirepo/internal/check"
)

// HealthLabel marks the tracking issue.
const HealthLabel = "velocirepo-health"

const issueTitle = "velocirepo: data health"

type issue struct {
	Number int    `json:"number"`
	Body   string `json:"body"`
}

// tracker keeps the tracking issue: its body is rewritten every night, it's
// commented on only when findings change, and it's closed when none are
// left.
type tracker struct {
	api githubAPI
	// owners are assigned when they're users, and mentioned when they're
	// teams (@org/team).
	owners []string
	runURL string
}

func (t tracker) find(ctx context.Context) (*issue, error) {
	var list []issue
	if _, err := t.api.call(ctx, "list issues", "issues: write", http.MethodGet, "/issues?state=open&per_page=1&labels="+HealthLabel, nil, &list); err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	return &list[0], nil
}

// update brings the tracking issue in line with the open findings.
func (t tracker) update(ctx context.Context, c Changes) error {
	existing, err := t.find(ctx)
	if err != nil {
		return err
	}
	body := t.body(c.Open)
	switch {
	case existing == nil && len(c.Open) == 0:
		return nil
	case existing == nil:
		in := map[string]any{"title": issueTitle, "body": body, "labels": []string{HealthLabel}}
		if users := t.users(); len(users) > 0 {
			in["assignees"] = users
		}
		_, err := t.api.call(ctx, "open the tracking issue", "issues: write", http.MethodPost, "/issues", in, nil)
		return err
	}

	path := fmt.Sprintf("/issues/%d", existing.Number)
	if existing.Body != body {
		if _, err := t.api.call(ctx, "update the tracking issue", "issues: write", http.MethodPatch, path, map[string]any{"body": body}, nil); err != nil {
			return err
		}
	}
	if c.Changed() {
		if _, err := t.api.call(ctx, "comment on the tracking issue", "issues: write", http.MethodPost, path+"/comments", map[string]any{"body": t.comment(c)}, nil); err != nil {
			return err
		}
	}
	if len(c.Open) == 0 {
		_, err := t.api.call(ctx, "close the tracking issue", "issues: write", http.MethodPatch, path, map[string]any{"state": "closed", "state_reason": "completed"}, nil)
		return err
	}
	return nil
}

func (t tracker) users() []string {
	var out []string
	for _, o := range t.owners {
		if !strings.Contains(o, "/") {
			out = append(out, strings.TrimPrefix(o, "@"))
		}
	}
	return out
}

func (t tracker) teams() []string {
	var out []string
	for _, o := range t.owners {
		if strings.Contains(o, "/") {
			out = append(out, "@"+strings.TrimPrefix(o, "@"))
		}
	}
	return out
}

func (t tracker) body(open []OpenFinding) string {
	var b strings.Builder
	b.WriteString("velocirepo checks the data every night. This issue lists the findings that are open; it's rewritten by every run.\n\n")
	if len(open) == 0 {
		b.WriteString("No findings are open.\n")
		return b.String()
	}
	sorted := append([]OpenFinding(nil), open...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Severity != sorted[j].Severity {
			return sorted[i].Severity == check.Error
		}
		return sorted[i].ID < sorted[j].ID
	})
	b.WriteString("| | Check | Where | What | Since | ID |\n|---|---|---|---|---|---|\n")
	for _, f := range sorted {
		where := strings.Trim(f.Source+"/"+f.Project, "/")
		if f.Target != "" && f.Target != f.Project {
			where += " " + f.Target
		}
		what := f.Message
		if f.Commit != "" {
			what += " (commit " + shortSHA(f.Commit) + ")"
		}
		fmt.Fprintf(&b, "| %s | `%s` | %s | %s | %s | `%s` |\n", icon(f.Severity), f.Check, where, escapeCell(what), f.Since, f.ID)
	}
	b.WriteString("\nTo accept a finding, add its ID to `velocirepo/checks.toml`:\n\n```toml\n[[accept]]\nid = \"…\"\nreason = \"…\"\n```\n")
	if teams := t.teams(); len(teams) > 0 {
		fmt.Fprintf(&b, "\ncc %s\n", strings.Join(teams, " "))
	}
	if t.runURL != "" {
		fmt.Fprintf(&b, "\nLast updated by %s\n", t.runURL)
	}
	return b.String()
}

func (t tracker) comment(c Changes) string {
	var b strings.Builder
	for _, f := range c.New {
		fmt.Fprintf(&b, "- New: %s `%s` %s/%s: %s\n", icon(f.Severity), f.Check, f.Source, f.Project, f.Message)
	}
	for _, f := range c.Resolved {
		fmt.Fprintf(&b, "- Resolved: `%s` %s/%s: %s\n", f.Check, f.Source, f.Project, f.Message)
	}
	if len(c.Open) == 0 {
		b.WriteString("\nNo findings are left.\n")
	}
	if t.runURL != "" {
		fmt.Fprintf(&b, "\n%s\n", t.runURL)
	}
	return b.String()
}

func icon(s check.Severity) string {
	switch s {
	case check.Error:
		return "❌"
	case check.Warning:
		return "⚠️"
	}
	return "ℹ️"
}

func escapeCell(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s)
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
