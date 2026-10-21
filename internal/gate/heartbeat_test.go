package gate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeActions imitates the Actions runs and issues APIs.
type fakeActions struct {
	mu       sync.Mutex
	runs     []map[string]any // event, created_at
	issue    map[string]any
	comments []string
	queries  []string
}

func (f *fakeActions) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/runs"):
		f.queries = append(f.queries, r.URL.RawQuery)
		since, _ := time.Parse(time.RFC3339, strings.TrimPrefix(r.URL.Query().Get("created"), ">="))
		n := 0
		for _, run := range f.runs {
			if run["event"] == r.URL.Query().Get("event") && !run["created_at"].(time.Time).Before(since) {
				n++
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": n})
	case r.URL.Path == "/repos/org/data/issues" && r.Method == http.MethodGet:
		var list []map[string]any
		if f.issue != nil {
			list = append(list, f.issue)
		}
		_ = json.NewEncoder(w).Encode(list)
	case r.URL.Path == "/repos/org/data/issues" && r.Method == http.MethodPost:
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		in["number"] = 7
		in["created_at"] = time.Date(2026, 10, 21, 12, 30, 0, 0, time.UTC)
		f.issue = in
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("{}"))
	case strings.HasSuffix(r.URL.Path, "/comments") && r.Method == http.MethodPost:
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.comments = append(f.comments, in["body"])
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("{}"))
	case strings.HasSuffix(r.URL.Path, "/comments"):
		var list []map[string]string
		for _, c := range f.comments {
			list = append(list, map[string]string{"body": c})
		}
		_ = json.NewEncoder(w).Encode(list)
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
	}
}

func heartbeatAt(t *testing.T, f *fakeActions, now time.Time) HeartbeatResult {
	t.Helper()
	srv := httptest.NewServer(f)
	defer srv.Close()
	res, err := NewHeartbeat("org/data", "workflow-token", srv.URL, "velocirepo.yml", "30 9 * * *", now).Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestHeartbeatMissedNight(t *testing.T) {
	f := &fakeActions{runs: []map[string]any{{"event": "schedule", "created_at": time.Date(2026, 10, 20, 9, 41, 0, 0, time.UTC)}}}
	res := heartbeatAt(t, f, time.Date(2026, 10, 21, 12, 30, 0, 0, time.UTC))
	if res.Ran || f.issue == nil || !strings.Contains(f.issue["body"].(string), "2026-10-21 09:30") {
		t.Errorf("result %+v, issue %+v; want the missed night reported the same day", res, f.issue)
	}
}

// A run still in progress counts; a manual run doesn't.
func TestHeartbeatRuns(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 30, 0, 0, time.UTC)
	f := &fakeActions{runs: []map[string]any{{"event": "schedule", "status": "in_progress", "created_at": time.Date(2026, 10, 21, 9, 52, 0, 0, time.UTC)}}}
	if res := heartbeatAt(t, f, now); !res.Ran || f.issue != nil {
		t.Errorf("a run in progress didn't count: %+v", res)
	}
	f = &fakeActions{runs: []map[string]any{{"event": "workflow_dispatch", "created_at": time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)}}}
	if res := heartbeatAt(t, f, now); res.Ran {
		t.Error("a manual run hid the missed night")
	}
}

// With the issue open for three days, one escalation comment is added, once.
func TestHeartbeatEscalatesOnce(t *testing.T) {
	f := &fakeActions{issue: map[string]any{"number": 7, "created_at": time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC), "assignees": []map[string]string{{"login": "jeroenjanssens"}}}}
	if res := heartbeatAt(t, f, time.Date(2026, 10, 19, 12, 30, 0, 0, time.UTC)); res.Escalated {
		t.Error("escalated before three days")
	}
	res := heartbeatAt(t, f, time.Date(2026, 10, 20, 12, 30, 0, 0, time.UTC))
	if !res.Escalated || !strings.Contains(f.comments[len(f.comments)-1], "@jeroenjanssens") {
		t.Errorf("result %+v, comments %q", res, f.comments)
	}
	if res := heartbeatAt(t, f, time.Date(2026, 10, 21, 12, 30, 0, 0, time.UTC)); res.Escalated {
		t.Error("escalated twice")
	}
	if len(f.comments) != 4 {
		t.Errorf("comments = %q, want three misses and one escalation", f.comments)
	}
}

func TestLastScheduledAndWorkflowCron(t *testing.T) {
	got, err := lastScheduled("30 9 * * *", time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC))
	if err != nil || !got.Equal(time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("before today's run: %v, %v", got, err)
	}
	path := filepath.Join(t.TempDir(), "velocirepo.yml")
	_ = os.WriteFile(path, []byte("on:\n  schedule:\n    - cron: '30 13 * * *'\n"), 0o644)
	if cron, err := WorkflowCron(path); err != nil || cron != "30 13 * * *" {
		t.Errorf("cron = %q, %v", cron, err)
	}
}
