package check

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/posit-dev/velocirepo/internal/config"
)

// repo is a Git repository with a velocirepo data directory.
type repo struct {
	t       *testing.T
	root    string
	dataDir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git isn't installed")
	}
	root := t.TempDir()
	r := &repo{t: t, root: root, dataDir: filepath.Join(root, "velocirepo", "data")}
	r.git("init", "-q", "-b", "main")
	r.git("config", "user.email", "test@example.com")
	r.git("config", "user.name", "Test")
	r.git("config", "commit.gpgsign", "false")
	r.write(".schema-version", "7\n")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", r.root}, args...)...).CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// write writes a file in the data directory.
func (r *repo) write(rel, content string) {
	r.t.Helper()
	path := filepath.Join(r.dataDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) remove(rel string) {
	r.t.Helper()
	if err := os.RemoveAll(filepath.Join(r.dataDir, filepath.FromSlash(rel))); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) commit(msg string) string {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", msg)
	return r.git("rev-parse", "HEAD")
}

// diff runs the diff checks on the work tree against HEAD, or between two
// commits.
func (r *repo) diff(cfg *config.Config, d Diff) []Finding {
	r.t.Helper()
	if d.Base == "" {
		d.Base = "HEAD"
	}
	findings, err := diffChecks{}.Run(context.Background(), &Context{Config: cfg, DataDir: r.dataDir, Now: now, Diff: &d})
	if err != nil {
		r.t.Fatal(err)
	}
	return findings
}

func metric(target, name, date string, value int) string {
	return fmt.Sprintf(`{"source":"github","metric":%q,"project_id":"positron","target":%q,"date":%q,"value":%d}`+"\n", name, target, date, value)
}

func event(typ, datetime, user string, ref int) string {
	return fmt.Sprintf(`{"source":"github","type":%q,"project_id":"positron","target":"posit-dev/positron","datetime":%q,"ref":%d,"user":%q}`+"\n", typ, datetime, ref, user)
}

func cfgWith(projects ...string) *config.Config {
	cfg := &config.Config{Projects: map[string]config.Project{}}
	for _, p := range projects {
		cfg.Projects[p] = config.Project{}
	}
	return cfg
}

func bySeverity(fs []Finding, sev Severity) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Severity == sev {
			out = append(out, f)
		}
	}
	return out
}

func requireOnly(t *testing.T, fs []Finding, check string, sev Severity) Finding {
	t.Helper()
	if len(fs) != 1 || fs[0].Check != check || fs[0].Severity != sev {
		t.Fatalf("findings = %+v, want one %s %s", fs, sev, check)
	}
	return fs[0]
}

const eventsDir = "events/github/positron/"

func eventHistory() string {
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		b.WriteString(event("issue", fmt.Sprintf("2026-07-%02dT10:00:00Z", i), "alice", i))
	}
	return b.String()
}

// The deletion of 2026-07-28: a full re-fetch replaced the history.
func TestDiffDeletedHistory(t *testing.T) {
	r := newRepo(t)
	r.write(eventsDir+"2026-07.jsonl", eventHistory())
	r.commit("history")
	r.write(eventsDir+"2026-07.jsonl", event("issue", "2026-07-20T10:00:00Z", "alice", 20))

	f := requireOnly(t, r.diff(cfgWith("positron"), Diff{}), "diff.records-removed", Error)
	if !strings.Contains(f.Message, "19 events removed (issue: 19), 2026-07-01 to 2026-07-19") {
		t.Errorf("message = %q", f.Message)
	}
	if f.Project != "positron" || f.Target != "posit-dev/positron" || f.Paths[0] != eventsDir {
		t.Errorf("finding = %+v", f)
	}
}

// The 44,619 total_reactions: a write dropped the reaction counts.
func TestDiffDroppedReactionCounts(t *testing.T) {
	r := newRepo(t)
	r.write("metrics/github/positron/2026-10-01.jsonl", metric("posit-dev/positron#1", "total_reactions", "2026-10-01", 3)+metric("posit-dev/positron", "daily_stars", "2026-10-01", 7))
	r.commit("night")
	r.write("metrics/github/positron/2026-10-01.jsonl", metric("posit-dev/positron", "daily_stars", "2026-10-01", 7))

	f := requireOnly(t, r.diff(cfgWith("positron"), Diff{}), "diff.records-removed", Error)
	if !strings.Contains(f.Message, "total_reactions: 1") {
		t.Errorf("message = %q", f.Message)
	}
}

func TestDiffNormalNight(t *testing.T) {
	r := newRepo(t)
	r.write("metrics/github/positron/2026-10-19.jsonl", metric("posit-dev/positron", "daily_stars", "2026-10-19", 7))
	r.write(eventsDir+"2026-10-19.jsonl", event("issue", "2026-10-19T10:00:00Z", "alice", 1))
	r.commit("night")
	r.write("metrics/github/positron/2026-10-20.jsonl", metric("posit-dev/positron", "daily_stars", "2026-10-20", 5))
	r.write(eventsDir+"2026-10-20.jsonl", event("comment", "2026-10-20T10:00:00Z", "bob", 1))
	r.write("metrics/github/positron/_watermark.json", `{"source":"github","project_id":"positron","target":"posit-dev/positron","date":"2026-10-20"}`+"\n")
	if fs := r.diff(cfgWith("positron"), Diff{}); len(fs) != 0 {
		t.Errorf("a normal night reported %+v", fs)
	}
}

func TestDiffChangedValue(t *testing.T) {
	r := newRepo(t)
	r.write("metrics/github/positron/2026-10-19.jsonl", metric("posit-dev/positron", "daily_stars", "2026-10-19", 7))
	r.commit("night")
	r.write("metrics/github/positron/2026-10-19.jsonl", metric("posit-dev/positron", "daily_stars", "2026-10-19", 14))
	requireOnly(t, r.diff(cfgWith("positron"), Diff{}), "diff.values-changed", Error)
}

// Concatenation moves records between files, which isn't a change; one that
// loses a record is.
func TestDiffConcatenation(t *testing.T) {
	r := newRepo(t)
	var month strings.Builder
	for d := 1; d <= 31; d++ {
		line := metric("posit-dev/positron", "daily_stars", fmt.Sprintf("2026-10-%02d", d), d)
		r.write(fmt.Sprintf("metrics/github/positron/2026-10-%02d.jsonl", d), line)
		month.WriteString(line)
	}
	r.commit("nights")
	for d := 1; d <= 31; d++ {
		r.remove(fmt.Sprintf("metrics/github/positron/2026-10-%02d.jsonl", d))
	}
	r.write("metrics/github/positron/2026-10.jsonl", month.String())
	if fs := r.diff(cfgWith("positron"), Diff{}); len(fs) != 0 {
		t.Errorf("concatenation reported %+v", fs)
	}
	moved, err := RecordsMoved(context.Background(), r.dataDir, Diff{Base: "HEAD"})
	if err != nil || len(moved) != 0 {
		t.Errorf("records-moved = %+v, %v", moved, err)
	}

	r.write("metrics/github/positron/2026-10.jsonl", strings.SplitAfterN(month.String(), "\n", 2)[1])
	moved, err = RecordsMoved(context.Background(), r.dataDir, Diff{Base: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	if f := requireOnly(t, moved, "concatenate.records-moved", Error); !strings.Contains(f.Message, "1 removed") {
		t.Errorf("message = %q", f.Message)
	}
}

func content(id, title string) string {
	return fmt.Sprintf(`{"source":"youtube","project_id":"posit","target":"@PositPBC","id":%q,"title":%q,"published_at":"2026-10-01T00:00:00Z"}`+"\n", id, title)
}

func TestDiffContent(t *testing.T) {
	r := newRepo(t)
	r.write("content/youtube/posit/videos.jsonl", content("a", "First")+content("b", "Second"))
	r.commit("content")
	r.write("content/youtube/posit/videos.jsonl", content("a", "First, renamed")+content("b", "Second"))
	if fs := r.diff(cfgWith("posit"), Diff{}); len(fs) != 0 {
		t.Errorf("content updated in place reported %+v", fs)
	}
	r.write("content/youtube/posit/videos.jsonl", content("a", "First"))
	requireOnly(t, r.diff(cfgWith("posit"), Diff{}), "diff.content-removed", Error)
}

// Deleted comments and forks disappear upstream; that's allowed.
func TestDiffAllowedRemovals(t *testing.T) {
	r := newRepo(t)
	r.write(eventsDir+"2026-10-19.jsonl", event("comment", "2026-10-19T10:00:00Z", "bob", 1)+event("fork", "2026-10-19T11:00:00Z", "carol", 0))
	r.commit("night")
	r.write(eventsDir+"2026-10-19.jsonl", "")
	f := requireOnly(t, r.diff(cfgWith("positron"), Diff{}), "diff.records-removed", Warning)
	if !strings.Contains(f.Message, "deleted upstream") {
		t.Errorf("message = %q", f.Message)
	}
}

// A repair's changes within its scope, removals and moved records included,
// are warnings; outside it they're still errors.
func TestDiffRepair(t *testing.T) {
	r := newRepo(t)
	r.write(eventsDir+"2026-07.jsonl", eventHistory())
	r.write("metrics/github/positron/2026-07.jsonl", metric("posit-dev/positron", "daily_stars", "2026-07-05", 3)+metric("posit-dev/positron", "daily_stars", "2026-07-25", 3))
	base := r.commit("history")

	// A corrected event: the same issue with another author is a removal
	// and an addition. A star moved to another day, and a day outside the
	// repair's range changed.
	history := strings.Replace(eventHistory(), `"user":"alice"}`, `"user":"alicia"}`, 1)
	r.write(eventsDir+"2026-07.jsonl", history)
	r.write("metrics/github/positron/2026-07.jsonl", metric("posit-dev/positron", "daily_stars", "2026-07-06", 3)+metric("posit-dev/positron", "daily_stars", "2026-07-25", 4))
	head := r.commit("Repair github/positron 2026-07-01..2026-07-10\n\n" + RepairTrailer + ": github/positron 2026-07-01..2026-07-10")

	fs := r.diff(cfgWith("positron"), Diff{Base: base, Head: head})
	if errs := bySeverity(fs, Error); len(errs) != 1 || errs[0].Check != "diff.values-changed" || !strings.Contains(errs[0].Message, "2026-07-25") {
		t.Errorf("errors = %+v, want only the change outside the repair", errs)
	}
	repaired := 0
	for _, f := range bySeverity(fs, Warning) {
		if f.Check == "diff.repaired" {
			repaired++
		}
	}
	if repaired != 2 {
		t.Errorf("findings = %+v, want the repaired event and star as diff.repaired", fs)
	}
}

// Comparing two commits finds a deletion spread over the commits between
// them.
func TestDiffBetweenCommits(t *testing.T) {
	r := newRepo(t)
	r.write(eventsDir+"2026-07.jsonl", eventHistory())
	base := r.commit("history")
	lines := strings.SplitAfter(eventHistory(), "\n")
	for i := 1; i <= 3; i++ {
		r.write(eventsDir+"2026-07.jsonl", strings.Join(lines[i*5:], ""))
		r.commit(fmt.Sprintf("by hand %d", i))
	}
	head := r.git("rev-parse", "HEAD")
	r.write(eventsDir+"2026-07.jsonl", eventHistory())

	f := requireOnly(t, r.diff(cfgWith("positron"), Diff{Base: base, Head: head}), "diff.records-removed", Error)
	if !strings.HasPrefix(f.Message, "15 events removed") {
		t.Errorf("message = %q", f.Message)
	}
}

func TestDiffRenameAndRemoveProject(t *testing.T) {
	r := newRepo(t)
	r.write("metrics/github/old/2026-10-19.jsonl", strings.ReplaceAll(metric("posit-dev/positron", "daily_stars", "2026-10-19", 7), "positron\",\"target", "old\",\"target"))
	r.write("metrics/pypi/gone/2026-10-19.jsonl", `{"source":"pypi","metric":"daily_downloads","project_id":"gone","target":"pkg","date":"2026-10-19","value":1}`+"\n")
	r.commit("before")
	r.remove("metrics/github/old")
	r.remove("metrics/pypi/gone")
	r.write("metrics/github/new/2026-10-19.jsonl", strings.ReplaceAll(metric("posit-dev/positron", "daily_stars", "2026-10-19", 7), "positron\",\"target", "new\",\"target"))

	fs := r.diff(cfgWith("new"), Diff{})
	if len(bySeverity(fs, Error))+len(bySeverity(fs, Warning)) != 0 || len(fs) != 2 {
		t.Fatalf("findings = %+v, want two notes", fs)
	}
	for _, f := range fs {
		if f.Project == "old" && !strings.Contains(f.Message, "renamed to new") {
			t.Errorf("rename not recognised: %q", f.Message)
		}
		if f.Project == "gone" && !strings.Contains(f.Message, "no longer in the config") {
			t.Errorf("removal: %q", f.Message)
		}
	}
}

// A migration renames fields; the base is migrated before it's compared.
func TestDiffMigration(t *testing.T) {
	r := newRepo(t)
	r.write(".schema-version", "6\n")
	old := `{"source":"github","type":"issue","project_id":"positron","target":"posit-dev/positron","datetime":"2026-07-01T10:00:00Z","ref":1,"tags":{"user":"alice","label":"bug"}}` + "\n"
	r.write(eventsDir+"2026-07.jsonl", old)
	r.commit("schema 6")
	r.write(".schema-version", "7\n")
	r.write(eventsDir+"2026-07.jsonl", `{"source":"github","type":"issue","project_id":"positron","target":"posit-dev/positron","datetime":"2026-07-01T10:00:00Z","ref":1,"user":"alice","extra":{"label":"bug"}}`+"\n")
	if fs := r.diff(cfgWith("positron"), Diff{}); len(fs) != 0 {
		t.Errorf("a migration reported %+v", fs)
	}
}

// Only the changed projects' files are read.
func TestDiffReadsOnlyChangedProjects(t *testing.T) {
	r := newRepo(t)
	r.write("metrics/github/positron/2026-10-19.jsonl", metric("posit-dev/positron", "daily_stars", "2026-10-19", 7))
	r.write("metrics/github/other/2026-10-19.jsonl", metric("posit-dev/other", "daily_stars", "2026-10-19", 7))
	r.commit("night")
	r.write("metrics/github/positron/2026-10-20.jsonl", metric("posit-dev/positron", "daily_stars", "2026-10-20", 7))

	var read []string
	onRead = func(path string) { read = append(read, path) }
	defer func() { onRead = nil }()
	r.diff(cfgWith("positron", "other"), Diff{})
	for _, p := range read {
		if strings.Contains(p, "/other/") {
			t.Errorf("read %s, which didn't change", p)
		}
	}
	if len(read) == 0 {
		t.Error("nothing was read")
	}
}

// A duplicate added tonight is an error about the night's records; one that
// was stored already isn't the night's.
func TestDiffDuplicatesAdded(t *testing.T) {
	r := newRepo(t)
	line := metric("posit-dev/positron", "daily_stars", "2026-10-19", 7)
	r.write("metrics/github/positron/2026-10-19.jsonl", line+line)
	r.commit("a stored duplicate")
	r.write("metrics/github/positron/2026-10-19.jsonl", line+line+metric("posit-dev/positron", "daily_stars", "2026-10-20", 1))
	if fs := r.diff(cfgWith("positron"), Diff{}); len(fs) != 0 {
		t.Errorf("a stored duplicate in a file touched tonight was reported: %+v", fs)
	}
	added := metric("posit-dev/positron", "daily_stars", "2026-10-21", 2)
	r.write("metrics/github/positron/2026-10-21.jsonl", added+added)
	requireOnly(t, r.diff(cfgWith("positron"), Diff{}), "data.integrity", Error)
}
