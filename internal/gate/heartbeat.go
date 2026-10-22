package gate

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// escalationAfter is how long the tracking issue is open before the
// heartbeat escalates, once.
const escalationAfter = 3 * 24 * time.Hour

const escalationMarker = "<!-- velocirepo-heartbeat-escalation -->"

// Heartbeat checks that the nightly workflow ran.
type Heartbeat struct {
	API githubAPI
	// Workflow is the nightly workflow's file name, such as velocirepo.yml.
	Workflow string
	// Cron is its schedule; daily at a fixed time.
	Cron string
	Now  time.Time
}

// NewHeartbeat returns a heartbeat for a repository, with the workflow
// token.
func NewHeartbeat(repo, token, apiURL, workflow, cron string, now time.Time) Heartbeat {
	return Heartbeat{API: githubAPI{Token: token, Repo: repo, URL: apiURL}, Workflow: workflow, Cron: cron, Now: now}
}

var cronLine = regexp.MustCompile(`(?m)^\s*-\s*cron:\s*['"]?([^'"\n]+?)['"]?\s*$`)

// WorkflowCron reads the first schedule of a workflow file.
func WorkflowCron(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	m := cronLine.FindSubmatch(data)
	if m == nil {
		return "", fmt.Errorf("%s has no schedule", path)
	}
	return string(m[1]), nil
}

// lastScheduled returns the latest time at or before now that a daily
// schedule at a fixed time (in UTC) was due.
func lastScheduled(cron string, now time.Time) (time.Time, error) {
	fields := strings.Fields(cron)
	if len(fields) != 5 || fields[2] != "*" || fields[3] != "*" || fields[4] != "*" {
		return time.Time{}, fmt.Errorf("the heartbeat needs a daily schedule at a fixed time; got %q", cron)
	}
	minute, err1 := strconv.Atoi(fields[0])
	hour, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil {
		return time.Time{}, fmt.Errorf("the heartbeat needs a daily schedule at a fixed time; got %q", cron)
	}
	now = now.UTC()
	t := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, time.UTC)
	if t.After(now) {
		t = t.AddDate(0, 0, -1)
	}
	return t, nil
}

// HeartbeatResult is what the heartbeat found.
type HeartbeatResult struct {
	Scheduled time.Time
	// Ran is set when a scheduled run started since; one in progress counts.
	Ran       bool
	Escalated bool
}

// Check asks the Actions API whether the nightly workflow started a
// scheduled run since it was last due. Manual runs don't count, so a release
// run doesn't hide a missed night. When it didn't, the heartbeat comments on
// the tracking issue, or opens it, and escalates once the issue has been open
// for three days. It never rewrites the issue's body, which belongs to the
// gate.
func (h Heartbeat) Check(ctx context.Context) (HeartbeatResult, error) {
	var res HeartbeatResult
	scheduled, err := lastScheduled(h.Cron, h.Now)
	if err != nil {
		return res, err
	}
	res.Scheduled = scheduled

	var runs struct {
		TotalCount int `json:"total_count"`
	}
	q := url.Values{"event": {"schedule"}, "created": {">=" + scheduled.Format(time.RFC3339)}, "per_page": {"1"}}
	if _, err := h.API.call(ctx, "list the nightly workflow's runs", "actions: read", http.MethodGet,
		"/actions/workflows/"+url.PathEscape(h.Workflow)+"/runs?"+q.Encode(), nil, &runs); err != nil {
		return res, err
	}
	if runs.TotalCount > 0 {
		res.Ran = true
		return res, nil
	}

	msg := fmt.Sprintf("The nightly workflow %s didn't start its scheduled run of %s UTC. Is its schedule disabled? GitHub disables the schedules of repositories without activity for 60 days.",
		h.Workflow, scheduled.Format("2006-01-02 15:04"))
	var open []struct {
		Number    int       `json:"number"`
		CreatedAt time.Time `json:"created_at"`
		Assignees []struct {
			Login string `json:"login"`
		} `json:"assignees"`
	}
	if _, err := h.API.call(ctx, "list issues", "issues: write", http.MethodGet, "/issues?state=open&per_page=1&labels="+HealthLabel, nil, &open); err != nil {
		return res, err
	}
	if len(open) == 0 {
		in := map[string]any{"title": issueTitle, "body": msg, "labels": []string{HealthLabel}}
		_, err := h.API.call(ctx, "open the tracking issue", "issues: write", http.MethodPost, "/issues", in, nil)
		return res, err
	}
	issue := open[0]
	path := fmt.Sprintf("/issues/%d/comments", issue.Number)
	if _, err := h.API.call(ctx, "comment on the tracking issue", "issues: write", http.MethodPost, path, map[string]any{"body": msg}, nil); err != nil {
		return res, err
	}
	if h.Now.Sub(issue.CreatedAt) < escalationAfter {
		return res, nil
	}
	var comments []struct {
		Body string `json:"body"`
	}
	for page := 1; ; page++ {
		var list []struct {
			Body string `json:"body"`
		}
		if _, err := h.API.call(ctx, "read the tracking issue's comments", "issues: write", http.MethodGet, fmt.Sprintf("%s?per_page=100&page=%d", path, page), nil, &list); err != nil {
			return res, err
		}
		comments = append(comments, list...)
		if len(list) < 100 {
			break
		}
	}
	for _, c := range comments {
		if strings.Contains(c.Body, escalationMarker) {
			return res, nil
		}
	}
	var mentions []string
	for _, a := range issue.Assignees {
		mentions = append(mentions, "@"+a.Login)
	}
	body := escalationMarker + "\nThis issue has been open for three days or more, and the nightly run is still missing. " + strings.Join(mentions, " ")
	if _, err := h.API.call(ctx, "escalate on the tracking issue", "issues: write", http.MethodPost, path, map[string]any{"body": strings.TrimSpace(body)}, nil); err != nil {
		return res, err
	}
	res.Escalated = true
	return res, nil
}
