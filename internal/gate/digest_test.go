package gate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/check"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

func TestSparkline(t *testing.T) {
	if got := sparkline([]int64{0, 1, 4, 8, 0}); got != "·▂▅█·" {
		t.Errorf("sparkline = %q", got)
	}
}

// Weeks of nothing stand out: a project whose events stopped is listed
// first, marked, with dots for the empty weeks.
func TestDigest(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)
	start := digestStart(now)
	var events []source.Event
	for d := 0; d < 7*6; d++ {
		ref := d + 1
		events = append(events, source.Event{Type: "issue_open", ProjectID: "positron", Target: "posit-dev/positron", Datetime: start.AddDate(0, 0, d).Format("2006-01-02") + "T10:00:00Z", Ref: &ref})
	}
	if _, err := store.WriteEvents(dataDir, "github", "positron", events); err != nil {
		t.Fatal(err)
	}
	var records []source.Record
	for d := 0; d < 7*digestWeeks; d++ {
		records = append(records, source.Record{Metric: "daily_downloads", ProjectID: "dplyr", Target: "dplyr", Date: start.AddDate(0, 0, d).Format("2006-01-02"), Value: int64(d)})
	}
	if _, err := store.WriteRecords(dataDir, "cran", "dplyr", records); err != nil {
		t.Fatal(err)
	}
	rows, from, err := DigestRows(dataDir, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	state := State{Findings: []OpenFinding{{Finding: check.NewFinding("fetch.failed", check.Error, "github", "positron", "", "", "comments: 422"), Since: "2026-10-20"}}}
	body := RenderDigest(rows, from, state, now)
	lines := strings.Split(body, "\n")
	var table []string
	for _, l := range lines {
		if strings.HasPrefix(l, "| ⚠️ |") || strings.HasPrefix(l, "|  |") {
			table = append(table, l)
		}
	}
	if len(table) != 2 || !strings.Contains(table[0], "positron") || !strings.Contains(table[0], "██████··`") {
		t.Errorf("digest =\n%s", body)
	}
	if !strings.Contains(body, "1 finding(s) open, 1 of them new this week") || !strings.Contains(body, "comments: 422") {
		t.Errorf("digest =\n%s", body)
	}
}

// The digest is commented on its own issue, never on the tracking issue.
func TestPostDigest(t *testing.T) {
	w := newWorld(t)
	api := NewDigestAPI("org/data", "workflow-token", w.api.URL)
	for i := 0; i < 2; i++ {
		if err := PostDigest(context.Background(), api, "digest"); err != nil {
			t.Fatal(err)
		}
	}
	if len(w.issues.issues) != 1 || len(w.issues.comments) != 2 {
		t.Errorf("issues %+v, comments %v", w.issues.issues, w.issues.comments)
	}
	if labels := w.issues.issues[0]["labels"].([]any); labels[0] != DigestLabel {
		t.Errorf("labels = %v", labels)
	}
}
