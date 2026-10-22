package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/check"
	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/fetch"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

// world is a data repository with a remote, and a fake GitHub.
type world struct {
	t      *testing.T
	remote string
	work   string
	cfg    *config.Config
	issues *fakeIssues
	api    *httptest.Server
	day    time.Time
}

var gitEnv = []string{"GIT_AUTHOR_NAME=gate", "GIT_AUTHOR_EMAIL=gate@example.com", "GIT_COMMITTER_NAME=gate", "GIT_COMMITTER_EMAIL=gate@example.com"}

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), gitEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newWorld(t *testing.T) *world {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git isn't installed")
	}
	base := t.TempDir()
	w := &world{t: t, remote: filepath.Join(base, "remote.git"), work: filepath.Join(base, "work"), day: time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)}
	sh(t, base, "init", "-q", "--bare", "-b", "main", w.remote)
	sh(t, base, "clone", "-q", w.remote, w.work)
	sh(t, w.work, "config", "commit.gpgsign", "false")
	w.cfg = &config.Config{Data: config.DataConfig{Dir: filepath.Join(w.work, "velocirepo", "data")}, Projects: map[string]config.Project{
		"proj":  {PyPI: config.StringList{"pkg"}},
		"other": {PyPI: config.StringList{"lib"}},
	}}
	w.write("proj", "pkg", "2026-10-19", 3)
	w.write("other", "lib", "2026-10-19", 3)
	sh(t, w.work, "add", "-A")
	sh(t, w.work, "commit", "-q", "-m", "Start")
	sh(t, w.work, "push", "-q", "origin", "main")
	w.issues = &fakeIssues{}
	w.api = httptest.NewServer(w.issues)
	t.Cleanup(w.api.Close)
	return w
}

func (w *world) write(project, target, date string, value int64) {
	w.t.Helper()
	if _, err := store.WriteRecords(w.cfg.DataDir(), "pypi", project, []source.Record{{Metric: "daily_downloads", ProjectID: project, Target: target, Date: date, Value: value}}); err != nil {
		w.t.Fatal(err)
	}
	if err := store.MarkFetchedSeries(w.cfg.DataDir(), "pypi", project, "", date, []string{target}); err != nil {
		w.t.Fatal(err)
	}
}

// night runs the gate with a fetch that stores the previous day.
func (w *world) night(extra func()) Outcome {
	w.t.Helper()
	day := w.day
	w.day = w.day.AddDate(0, 0, 1)
	out, err := Run(context.Background(), Options{
		Config: w.cfg, Now: func() time.Time { return day }, Repo: "org/data", RepoToken: "workflow-token", APIURL: w.api.URL,
		Tokens: fetch.Tokens{GitHub: "personal-token"}, GitEnv: gitEnv, SummaryPath: filepath.Join(w.t.TempDir(), "summary.md"),
		Fetch: func(ctx context.Context, o fetch.Options) ([]fetch.Result, error) {
			yesterday := day.AddDate(0, 0, -1).Format("2006-01-02")
			w.write("proj", "pkg", yesterday, 5)
			w.write("other", "lib", yesterday, 5)
			if extra != nil {
				extra()
			}
			return []fetch.Result{{Source: "pypi", ProjectID: "proj", Records: 1}}, nil
		},
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// byHand commits a change in a second clone and pushes it, as a person
// would.
func (w *world) byHand(change func(dataDir string), msg string) string {
	w.t.Helper()
	other := filepath.Join(w.t.TempDir(), "other")
	sh(w.t, filepath.Dir(other), "clone", "-q", w.remote, other)
	change(filepath.Join(other, "velocirepo", "data"))
	sh(w.t, other, "add", "-A")
	sh(w.t, other, "commit", "-q", "-m", msg)
	sh(w.t, other, "push", "-q", "origin", "main")
	return sh(w.t, other, "rev-parse", "HEAD")
}

func (w *world) pull() {
	w.t.Helper()
	sh(w.t, w.work, "pull", "-q", "--rebase", "origin", "main")
}

type fakeIssues struct {
	mu       sync.Mutex
	issues   []map[string]any
	comments []string
	auth     []string
	refuse   bool
}

func (f *fakeIssues) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if f.refuse {
		http.Error(w, `{"message":"Resource not accessible by integration"}`, http.StatusForbidden)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var in map[string]any
	_ = json.Unmarshal(body, &in)
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/repos/org/data/issues":
		var open []map[string]any
		for _, i := range f.issues {
			if i["state"] == "open" {
				open = append(open, i)
			}
		}
		_ = json.NewEncoder(w).Encode(open)
	case r.Method == http.MethodPost && r.URL.Path == "/repos/org/data/issues":
		in["number"] = len(f.issues) + 1
		in["state"] = "open"
		f.issues = append(f.issues, in)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(in)
	case r.Method == http.MethodPatch:
		i := f.issues[len(f.issues)-1]
		for k, v := range in {
			i[k] = v
		}
		_ = json.NewEncoder(w).Encode(i)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
		f.comments = append(f.comments, in["body"].(string))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("{}"))
	case strings.Contains(r.URL.Path, "/releases") || strings.Contains(r.URL.Path, "/actions/"):
		http.NotFound(w, r)
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
	}
}

func remoteHead(t *testing.T, w *world, ref string) string {
	return sh(t, w.remote, "rev-parse", ref)
}

// The first run starts checking at HEAD, commits the night's data with the
// run's date, and pushes it with the checked ref.
func TestGateFirstRun(t *testing.T) {
	w := newWorld(t)
	out := w.night(nil)
	if out.DataCommit == "" || !out.Pushed {
		t.Fatalf("outcome = %+v", out)
	}
	msg := sh(t, w.work, "log", "-1", "--format=%B")
	if !strings.HasPrefix(msg, "Fetch metrics on 2026-10-21\n\nStored through:\n- pypi: 2026-10-20") {
		t.Errorf("message = %q", msg)
	}
	if remoteHead(t, w, "main") != remoteHead(t, w, checkedRef) {
		t.Error("the checked ref isn't at the pushed commit")
	}
	if len(w.issues.issues) != 0 {
		t.Errorf("an issue was opened without findings: %+v", w.issues.issues)
	}
}

// A commit made by hand that deletes a project's data is reported the next
// night, and the finding stays open until the records are back. The issue
// is opened, updated without comment while nothing changes, commented on
// when something does, and closed.
func TestGateCommitsMadeByHand(t *testing.T) {
	w := newWorld(t)
	w.night(nil)
	saved, err := os.ReadFile(filepath.Join(w.cfg.DataDir(), "metrics/pypi/proj/2026-10-19.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	bad := w.byHand(func(dataDir string) {
		_ = os.Remove(filepath.Join(dataDir, "metrics/pypi/proj/2026-10-19.jsonl"))
	}, "Clean up by hand")
	w.pull()

	out := w.night(nil)
	if len(out.Hand) != 1 || out.Hand[0].Check != "diff.records-removed" || out.Hand[0].Commit != bad {
		t.Fatalf("hand findings = %+v", out.Hand)
	}
	if !out.Failed() || len(w.issues.issues) != 1 || w.issues.issues[0]["state"] != "open" {
		t.Fatalf("issues = %+v, failed = %v", w.issues.issues, out.Failed())
	}
	if labels := w.issues.issues[0]["labels"].([]any); labels[0] != HealthLabel {
		t.Errorf("labels = %v", labels)
	}

	out = w.night(nil)
	if len(out.Changes.Open) != 1 || len(out.Changes.New) != 0 {
		t.Fatalf("second night: %+v", out.Changes)
	}
	if len(w.issues.comments) != 0 {
		t.Errorf("commented without a change: %v", w.issues.comments)
	}

	w.byHand(func(dataDir string) {
		_ = os.WriteFile(filepath.Join(dataDir, "metrics/pypi/proj/2026-10-19.jsonl"), saved, 0o644)
	}, "Restore the records")
	w.pull()
	out = w.night(nil)
	if len(out.Changes.Open) != 0 || len(out.Changes.Resolved) != 1 {
		t.Fatalf("after the restore: %+v", out.Changes)
	}
	if len(w.issues.comments) != 1 || w.issues.issues[0]["state"] != "closed" {
		t.Errorf("issue = %+v, comments = %v; want one comment and closed", w.issues.issues[0], w.issues.comments)
	}
	for _, a := range w.issues.auth {
		if a != "Bearer workflow-token" {
			t.Fatalf("the issues API was called with %q, not the workflow token", a)
		}
	}
}

func TestGateIssuePermission(t *testing.T) {
	w := newWorld(t)
	w.night(nil)
	w.byHand(func(dataDir string) {
		_ = os.Remove(filepath.Join(dataDir, "metrics/pypi/proj/2026-10-19.jsonl"))
	}, "Clean up by hand")
	w.pull()
	w.issues.refuse = true
	out := w.night(nil)
	found := false
	for _, f := range out.Other {
		found = found || (f.Check == "gate.permission" && strings.Contains(f.Message, "issues: write"))
	}
	if !found {
		t.Errorf("other findings = %+v, want one naming issues: write", out.Other)
	}
}

// Someone pushes during the run: their commit is checked, the gate's
// commits are rebased onto it, and the checked ref ends at the pushed
// commit, after the other one.
func TestGateMovingMain(t *testing.T) {
	for _, c := range []struct {
		name    string
		project string
	}{{"another project", "other"}, {"the same project", "proj"}} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.night(nil)
			var theirs string
			out := w.night(func() {
				theirs = w.byHand(func(dataDir string) {
					_ = os.Remove(filepath.Join(dataDir, "metrics/pypi/"+c.project+"/2026-10-19.jsonl"))
				}, "Pushed during the run")
			})
			if !out.Pushed {
				t.Fatal("not pushed")
			}
			head := remoteHead(t, w, "main")
			if head != remoteHead(t, w, checkedRef) {
				t.Error("the checked ref isn't at the pushed commit")
			}
			if sh(t, w.remote, "merge-base", "--is-ancestor", theirs, head) != "" {
				t.Error("the other commit isn't in the history")
			}
			found := false
			for _, f := range out.Hand {
				found = found || (f.Commit == theirs && f.Project == c.project && f.Check == "diff.records-removed")
			}
			if !found {
				t.Errorf("hand findings = %+v, want the removal in %s", out.Hand, theirs)
			}
		})
	}
}

// A force-pushed main leaves the checked ref outside the history: it's
// reported and reset.
func TestGateForcePush(t *testing.T) {
	w := newWorld(t)
	w.night(nil)
	other := filepath.Join(t.TempDir(), "other")
	sh(t, filepath.Dir(other), "clone", "-q", w.remote, other)
	sh(t, other, "reset", "-q", "--hard", "HEAD~1")
	sh(t, other, "commit", "-q", "--allow-empty", "-m", "Rewritten")
	sh(t, other, "push", "-q", "--force", "origin", "main")
	sh(t, w.work, "fetch", "-q", "origin")
	sh(t, w.work, "reset", "-q", "--hard", "origin/main")

	out := w.night(nil)
	found := false
	for _, f := range out.Other {
		found = found || f.Check == "gate.checked-ref"
	}
	if !found {
		t.Errorf("other findings = %+v, want gate.checked-ref", out.Other)
	}
	if remoteHead(t, w, "main") != remoteHead(t, w, checkedRef) {
		t.Error("the checked ref wasn't reset")
	}
}

// Concatenation happens on or after the 8th, in its own commit.
func TestGateConcatenation(t *testing.T) {
	w := newWorld(t)
	for d := 1; d <= 31; d++ {
		w.write("proj", "pkg", fmt.Sprintf("2026-10-%02d", d), 1)
		w.write("other", "lib", fmt.Sprintf("2026-10-%02d", d), 1)
	}
	sh(t, w.work, "add", "-A")
	sh(t, w.work, "commit", "-q", "-m", "October")
	w.day = time.Date(2026, 11, 7, 9, 30, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		w.write("proj", "pkg", fmt.Sprintf("2026-11-%02d", i+1), 1)
		w.write("other", "lib", fmt.Sprintf("2026-11-%02d", i+1), 1)
	}
	if out := w.night(nil); out.ConcatCommit != "" {
		t.Errorf("concatenated on the 7th")
	}
	out := w.night(nil)
	if out.ConcatCommit == "" || out.DataCommit == "" {
		t.Fatalf("outcome = %+v, want a data and a concatenation commit", out)
	}
	if msg := sh(t, w.work, "log", "-1", "--format=%s", out.ConcatCommit); msg != "Concatenate October 2026" {
		t.Errorf("message = %q", msg)
	}
	if sh(t, w.work, "rev-parse", out.ConcatCommit+"^") != out.DataCommit {
		t.Error("the concatenation commit doesn't follow the data commit")
	}
	if _, err := os.Stat(filepath.Join(w.cfg.DataDir(), "metrics/pypi/proj/2026-10.jsonl")); err != nil {
		t.Error(err)
	}
}

// The summary lists what enforcing would have done; everything is
// committed regardless.
func TestGateReportsWhatEnforcingWouldDo(t *testing.T) {
	night := check.Report{Findings: []check.Finding{
		check.NewFinding("diff.records-removed", check.Error, "github", "positron", "", "events", "57,155 events removed"),
		check.NewFinding("diff.values-changed", check.Error, "plausible", "quarto", "", "metrics", "changed"),
		check.NewFinding("data.integrity", check.Error, "plausible", "dplyr", "", "dup", "duplicates"),
		check.NewFinding("fetch.failed", check.Error, "cran", "dplyr", "", "", "502"),
	}}
	restore, keep, hold := wouldEnforce(night)
	if strings.Join(restore, ",") != "github/positron" || strings.Join(keep, ",") != "plausible/quarto" || strings.Join(hold, ",") != "plausible/dplyr" {
		t.Errorf("restore %v, keep %v, hold back %v", restore, keep, hold)
	}
}
