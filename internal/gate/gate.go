package gate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/posit-dev/velocirepo/internal/archive"
	"github.com/posit-dev/velocirepo/internal/check"
	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/fetch"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
	"github.com/posit-dev/velocirepo/internal/version"
)

// Options configure a gate run.
type Options struct {
	Config *config.Config
	// Now returns the current time; nil means time.Now.
	Now func() time.Time
	// Tokens are what velocirepo fetches with.
	Tokens fetch.Tokens
	// RepoToken is the workflow token, for the tracking issue and the
	// archive. Repo is the data repository, owner/name; without it, there's
	// no issue and no upload.
	RepoToken string
	Repo      string
	// APIURL is GitHub's API, for tests.
	APIURL string
	// Remote is the remote to push to; "origin" by default. A repository
	// without it is committed to, but not pushed.
	Remote string
	// Fetch runs the fetch; nil means fetch.All with Tokens.
	Fetch func(ctx context.Context, opts fetch.Options) ([]fetch.Result, error)
	// AfterFetch is called with the fetch's results, to write the fetch
	// log, for example.
	AfterFetch func(results []fetch.Result)
	// BuildDB rebuilds the database after data changes, if set.
	BuildDB func()
	// Reconcilers give reference totals; nil means none.
	Reconcilers map[string]source.Reconciler
	// ArchiveDir is where the run's archive is written, outside the work
	// tree; "" means no archive.
	ArchiveDir string
	// Store and Artifacts keep the archive; nil means GitHub's for Repo.
	Store     archive.Store
	Artifacts *archive.Artifacts
	// NoConcatenate skips concatenation.
	NoConcatenate bool
	// Release lands a held-back source/project regardless of its findings;
	// Discard deletes it, watermarks included.
	Release string
	Discard string
	// Repair, if set, makes the run a repair of a range instead of the
	// nightly fetch.
	Repair *check.Repair
	// Runs downloads into dir the archived runs a repair replays; nil means
	// from Repo's releases.
	Runs func(ctx context.Context, repair check.Repair, dir string) ([]string, error)
	// OutputPath and SummaryPath are GitHub Actions' step output and job
	// summary files, if set.
	OutputPath  string
	SummaryPath string
	// RunURL links the workflow run from the issue.
	RunURL string
	// GitEnv is added to Git's environment, such as the committer.
	GitEnv []string
	// Log gets the gate's progress and annotations.
	Log io.Writer
}

// Outcome is what a gate run did.
type Outcome struct {
	Fetch []fetch.Result
	// Night is the check of the night's changes; Hand are the findings
	// about commits made by hand.
	Night   check.Report
	Hand    []OpenFinding
	Changes Changes
	// What an enforcing gate would have done.
	WouldRestore  []string
	WouldKeep     []string
	WouldHoldBack []string
	// HeldBack are the sources and projects held back, as source/project.
	HeldBack     []string
	DataCommit   string
	ConcatCommit string
	Pushed       bool
	// Other are problems of the run itself, such as a refused permission.
	Other []check.Finding
}

// Failed reports whether the run should fail: an open error that isn't
// accepted.
func (o Outcome) Failed() bool {
	for _, f := range o.Changes.Open {
		if f.Severity == check.Error && !f.Accepted {
			return true
		}
	}
	return false
}

type run struct {
	opts     Options
	cfg      *config.Config
	git      git
	root     string
	dataDir  string
	dataRel  string
	veloRel  string
	now      time.Time
	accepted check.Acceptances
	log      io.Writer
}

// Run runs the gate. In this version it only reports what enforcing would
// do; it commits everything, as fetch always has.
func Run(ctx context.Context, opts Options) (Outcome, error) {
	var out Outcome
	r, err := newRun(ctx, opts)
	if err != nil {
		return out, err
	}

	statePath := filepath.Join(r.root, r.veloRel, StateFile)
	state, err := readState(statePath)
	if err != nil {
		return out, err
	}

	// 1. The commits since the last run, made by hand.
	r.fetchCheckedRef(ctx)
	head, err := r.git.head(ctx)
	if err != nil {
		return out, err
	}
	hand, other, err := r.checkHandCommits(ctx, head, state)
	if err != nil {
		return out, err
	}
	out.Hand, out.Other = hand, other

	// 2. Held-back data is overlaid, so the fetch continues from it and it's
	// checked again as a whole. That happens when reporting too, so held-back
	// data lands when enforcing is switched off. A repair leaves it alone.
	var held, notes []string
	if opts.Repair == nil {
		if held, notes, err = r.prepareHeldBack(); err != nil {
			return out, err
		}
	}

	// 3. The fetch, without concatenation, or the repair.
	var results []fetch.Result
	var stats *fetch.Stats
	var findings []check.Finding
	var diff check.Diff
	if opts.Repair != nil {
		results, stats, findings = r.repair(ctx, *opts.Repair)
		diff = check.Diff{Base: "HEAD", Repairs: []check.Repair{*opts.Repair}}
	} else {
		results, stats, findings = r.fetch(ctx)
		diff = check.Diff{Base: "HEAD"}
	}
	out.Fetch = results
	out.Other = append(out.Other, findings...)

	// 4. The night's changes.
	c := &check.Context{Config: r.cfg, DataDir: r.dataDir, Now: r.now, Diff: &diff, Reconcilers: opts.Reconcilers,
		Results: &fetch.RunResults{RunTime: r.now, Results: results, Quota: stats.QuotaSnapshot(), Deprecations: stats.DeprecationsSnapshot()}}
	night, err := check.Run(ctx, c, check.Default(), r.accepted)
	if err != nil {
		return out, fmt.Errorf("check: %w", err)
	}
	out.Night = night
	out.WouldRestore, out.WouldKeep, out.WouldHoldBack = wouldEnforce(night)
	if r.cfg.Settings.Enforce() && opts.Repair != nil {
		// A repair is deliberate: nothing is held back, and only changes
		// outside its range are undone.
		diffs, repairs, err := check.CompareData(ctx, r.dataDir, diff)
		if err != nil {
			return out, err
		}
		if err := r.applyRule(diffs, repairs); err != nil {
			return out, err
		}
	} else if r.cfg.Settings.Enforce() {
		heldNow, err := r.enforce(ctx, night)
		if err != nil {
			return out, err
		}
		out.HeldBack = heldNow
	}
	if err := r.landHeldBack(held, out.HeldBack); err != nil {
		return out, err
	}
	if r.opts.Release != "" {
		if contains(held, r.opts.Release) {
			notes = append(notes, "Released the held-back data of "+r.opts.Release+", regardless of its findings.")
		} else {
			notes = append(notes, "Nothing is held back for "+r.opts.Release+".")
		}
	}

	// 5. The data commit, with the state.
	all := r.openFindings(night.Findings, "")
	all = append(all, hand...)
	all = append(all, r.openFindings(out.Other, "")...)
	out.Changes = state.update(all, r.now.UTC().Format("2006-01-02"))
	out.Changes.Notes = notes
	out.Changes.HeldBack = out.HeldBack
	if err := writeState(statePath, state); err != nil {
		return out, err
	}
	freshness, _ := fetch.Freshness(r.cfg, r.dataDir, r.now)
	message := dataMessage(r.now, freshness, heldBackNotes(out.HeldBack, night, r.veloRel))
	if opts.Repair != nil {
		message = repairMessage(*opts.Repair)
	}
	out.DataCommit, err = r.git.commitPaths(ctx, message, r.veloRel)
	if err != nil {
		return out, err
	}

	// 6. Concatenation, in its own commit.
	if !opts.NoConcatenate && opts.Repair == nil {
		commit, findings, err := r.concatenate(ctx)
		if err != nil {
			return out, err
		}
		out.ConcatCommit = commit
		if len(findings) > 0 {
			extra := r.openFindings(findings, "")
			out.Other = append(out.Other, findings...)
			if err := r.recordFindings(ctx, statePath, &state, &out.Changes, extra); err != nil {
				return out, err
			}
		}
	}

	// 7. Push, and move the checked ref.
	raced, err := r.push(ctx, head)
	if err != nil {
		return out, err
	}
	out.Pushed = r.git.hasRemote(ctx, r.remote())
	if len(raced) > 0 {
		out.Hand = append(out.Hand, raced...)
		if err := r.recordFindings(ctx, statePath, &state, &out.Changes, raced); err != nil {
			return out, err
		}
		if _, err := r.push(ctx, ""); err != nil {
			return out, err
		}
	}

	// 8. The tracking issue and the job summary.
	if opts.Repo != "" {
		t := tracker{api: githubAPI{Token: opts.RepoToken, Repo: opts.Repo, URL: opts.APIURL}, owners: r.cfg.Settings.Owners, runURL: opts.RunURL}
		if err := t.update(ctx, out.Changes); err != nil {
			var perm *PermissionError
			if !errors.As(err, &perm) {
				return out, err
			}
			out.Other = append(out.Other, check.NewFinding("gate.permission", check.Warning, "", "", "", "issues", err.Error()))
		}
	}
	r.report(out)
	return out, nil
}

func newRun(ctx context.Context, opts Options) (*run, error) {
	if opts.Config == nil {
		return nil, errors.New("no config")
	}
	now := time.Now()
	if opts.Now != nil {
		now = opts.Now()
	}
	dataDir, err := filepath.Abs(opts.Config.DataDir())
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(dataDir); err == nil {
		dataDir = resolved
	}
	root, err := git{dir: dataDir}.run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("the data directory isn't in a Git repository: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	dataRel, err := filepath.Rel(root, dataDir)
	if err != nil {
		return nil, err
	}
	accepted, err := check.LoadAcceptances(filepath.Join(filepath.Dir(dataDir), "checks.toml"))
	if err != nil {
		return nil, err
	}
	log := opts.Log
	if log == nil {
		log = io.Discard
	}
	return &run{opts: opts, cfg: opts.Config, git: git{dir: root, env: opts.GitEnv}, root: root, dataDir: dataDir,
		dataRel: filepath.ToSlash(dataRel), veloRel: filepath.ToSlash(filepath.Dir(dataRel)), now: now, accepted: accepted, log: log}, nil
}

func (r *run) remote() string {
	if r.opts.Remote != "" {
		return r.opts.Remote
	}
	return "origin"
}

func (r *run) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.log, format+"\n", args...)
}

// fetchCheckedRef gets the checked ref from the remote; actions/checkout
// doesn't fetch it.
func (r *run) fetchCheckedRef(ctx context.Context) {
	if r.git.hasRemote(ctx, r.remote()) {
		_, _ = r.git.run(ctx, "fetch", "--quiet", r.remote(), "+"+checkedRef+":"+checkedRef)
	}
}

// openFindings applies the acceptances to findings and ties them to a commit.
func (r *run) openFindings(findings []check.Finding, commit string) []OpenFinding {
	out := make([]OpenFinding, 0, len(findings))
	for _, f := range findings {
		if a, ok := r.accepted.Match(f, r.now); ok {
			f.Accepted, f.Reason = true, a.Reason
		}
		out = append(out, OpenFinding{Finding: f, Commit: commit})
	}
	return out
}

// recordFindings adds findings that came up after the data commit, and
// commits the state again.
func (r *run) recordFindings(ctx context.Context, statePath string, state *State, changes *Changes, extra []OpenFinding) error {
	open := append(append([]OpenFinding(nil), changes.Open...), extra...)
	prevNew := changes.New
	prevResolved := changes.Resolved
	*state = State{Findings: changes.Open}
	c := state.update(open, r.now.UTC().Format("2006-01-02"))
	c.New = append(prevNew, c.New...)
	c.Resolved = prevResolved
	c.Notes, c.HeldBack = changes.Notes, changes.HeldBack
	*changes = c
	if err := writeState(statePath, *state); err != nil {
		return err
	}
	_, err := r.git.commitPaths(ctx, "Update the gate's state\n", joinRel(r.veloRel, StateFile))
	return err
}

func joinRel(dir, name string) string {
	if dir == "." || dir == "" {
		return name
	}
	return dir + "/" + name
}

// checkHandCommits checks the commits made since the checked ref, and keeps
// the findings about earlier ones open until the data has the removed
// records, or the original values, again.
func (r *run) checkHandCommits(ctx context.Context, head string, state State) ([]OpenFinding, []check.Finding, error) {
	checked := r.git.resolve(ctx, checkedRef)
	var other []check.Finding
	switch {
	case checked == "":
		r.logf("First run: checking from %s on", shortSHA(head))
		if _, err := r.git.run(ctx, "update-ref", checkedRef, head); err != nil {
			return nil, nil, err
		}
		return nil, nil, nil
	case !r.git.isAncestor(ctx, checked, head):
		other = append(other, check.NewFinding("gate.checked-ref", check.Warning, "", "", "", "",
			fmt.Sprintf("the last checked commit %s is no longer in the history, for example after a force-push; checking from %s on", shortSHA(checked), shortSHA(head))))
		if _, err := r.git.run(ctx, "update-ref", checkedRef, head); err != nil {
			return nil, nil, err
		}
		checked = head
	}

	var out []OpenFinding
	commits, err := r.git.commitsSince(ctx, checked, head)
	if err != nil {
		return nil, nil, err
	}
	fresh := map[string]bool{}
	for _, c := range commits {
		fresh[c] = true
		findings, err := r.diffCommit(ctx, c+"^", c, c)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, r.openFindings(findings, c)...)
	}

	// Earlier commits' findings stay open while the work tree still lacks
	// what they removed.
	byCommit := map[string]map[string]bool{}
	for _, f := range state.Findings {
		if f.Commit != "" && !fresh[f.Commit] {
			if byCommit[f.Commit] == nil {
				byCommit[f.Commit] = map[string]bool{}
			}
			byCommit[f.Commit][f.ID] = true
		}
	}
	for commit, ids := range byCommit {
		if r.git.resolve(ctx, commit) == "" {
			continue
		}
		findings, err := r.diffCommit(ctx, commit+"^", "", commit)
		if err != nil {
			return nil, nil, err
		}
		for _, f := range r.openFindings(findings, commit) {
			if ids[f.ID] {
				out = append(out, f)
			}
		}
	}
	return out, other, nil
}

// diffCommit runs the diff checks between two versions, with the findings
// tied to a commit made by hand, so they keep their IDs from night to night.
func (r *run) diffCommit(ctx context.Context, base, head, commit string) ([]check.Finding, error) {
	diffs, repairs, err := check.CompareData(ctx, r.dataDir, check.Diff{Base: base, Head: head})
	if err != nil {
		return nil, err
	}
	if head == "" {
		// Against the work tree, only the commit's own repairs apply.
		_, repairs, err = check.CompareData(ctx, r.dataDir, check.Diff{Base: base, Head: commit})
		if err != nil {
			return nil, err
		}
	}
	var out []check.Finding
	for _, f := range check.DiffFindings(r.cfg, diffs, repairs) {
		if head == "" && f.Check == "diff.repaired" {
			continue
		}
		g := check.NewFinding(f.Check, f.Severity, f.Source, f.Project, f.Target, f.Key+"@"+commit, f.Message+" in commit "+shortSHA(commit)).WithPaths(f.Paths...)
		out = append(out, g)
	}
	return out, nil
}

func (r *run) fetch(ctx context.Context) ([]fetch.Result, *fetch.Stats, []check.Finding) {
	stats := fetch.NewStats(0)
	fopts := fetch.Options{NoConcatenate: true, Quiet: true, Stats: stats, Now: func() time.Time { return r.now }}
	var w *archive.Writer
	if r.opts.ArchiveDir != "" && r.cfg.Settings.ArchiveEnabled() {
		var err error
		w, err = archive.NewWriter(r.opts.ArchiveDir, archive.NewRunID(r.now, os.Getenv), version.Version, r.now)
		if err != nil {
			r.logf("::warning::archive: %v", err)
			w = nil
		} else {
			fetch.AttachArchive(&fopts, w)
		}
	}
	run := r.opts.Fetch
	if run == nil {
		run = func(ctx context.Context, o fetch.Options) ([]fetch.Result, error) {
			return fetch.All(ctx, r.cfg, r.opts.Tokens, o)
		}
	}
	results, err := run(ctx, fopts)
	if err != nil {
		results = append(results, fetch.Result{Error: err.Error()})
	}
	if r.opts.AfterFetch != nil {
		r.opts.AfterFetch(results)
	}
	if r.opts.BuildDB != nil {
		r.opts.BuildDB()
	}
	return results, stats, r.storeArchive(ctx, w)
}

// storeArchive uploads the run's archive and any earlier ones that are
// pending. A failed upload doesn't hold data back: the archive is saved as
// a workflow artifact for the next run.
func (r *run) storeArchive(ctx context.Context, w *archive.Writer) []check.Finding {
	if r.opts.ArchiveDir == "" || !r.cfg.Settings.ArchiveEnabled() {
		return nil
	}
	var out []check.Finding
	if w != nil {
		if _, err := w.Close(); err != nil {
			r.logf("::warning::close archive: %v", err)
		}
		t := r.opts.Tokens
		found, err := archive.FindSecrets(w.Dir(), []string{t.GitHub, t.Plausible, t.YouTube, t.LinkedIn, r.opts.RepoToken})
		if err != nil || len(found) > 0 {
			_ = os.RemoveAll(w.Dir())
			msg := fmt.Sprintf("the archive contained a token (in %s), so it was neither uploaded nor saved", strings.Join(found, ", "))
			if err != nil {
				msg = "couldn't check the archive for tokens, so it was deleted: " + err.Error()
			}
			out = append(out, check.NewFinding("archive.secret", check.Error, strings.Join(found, ","), "", "", "", msg))
			w = nil
		}
	}
	st, arts := r.opts.Store, r.opts.Artifacts
	if r.opts.Repo != "" {
		rs := &archive.ReleaseStore{Token: r.opts.RepoToken, Repo: r.opts.Repo, APIURL: r.opts.APIURL, UploadURL: r.opts.APIURL}
		if st == nil {
			st = rs
		}
		if arts == nil {
			arts = &archive.Artifacts{Token: r.opts.RepoToken, Repo: r.opts.Repo, APIURL: r.opts.APIURL}
		}
	}
	if st == nil {
		return out
	}
	if w != nil {
		if err := st.Upload(ctx, w.Dir()); err != nil {
			out = append(out, check.NewFinding("archive.upload", check.Warning, "", "", "", "", "the archive couldn't be uploaded, and is saved for the next run: "+err.Error()))
			if r.opts.OutputPath != "" {
				_ = appendOutput(r.opts.OutputPath, "pending-archive", w.Dir())
				_ = appendOutput(r.opts.OutputPath, "pending-name", archive.ArtifactName(filepath.Base(w.Dir())))
			}
		}
	}
	if arts != nil {
		work, err := os.MkdirTemp("", "velocirepo-pending-")
		if err == nil {
			defer func() { _ = os.RemoveAll(work) }()
			t := r.opts.Tokens
			result, err := archive.CatchUpPending(ctx, arts, st, work, []string{t.GitHub, t.Plausible, t.YouTube, t.LinkedIn, r.opts.RepoToken}, r.now)
			if err != nil {
				out = append(out, check.NewFinding("archive.pending", check.Warning, "", "", "", "list", "pending archives couldn't be listed: "+err.Error()))
			}
			for _, f := range result.Failed {
				sev := check.Warning
				if f.Overdue() {
					sev = check.Error
				}
				out = append(out, check.NewFinding("archive.pending", sev, "", "", "", f.RunID, fmt.Sprintf("the archive of %s has waited %s for its upload: %v", f.RunID, f.Age.Round(time.Hour), f.Err)))
			}
		}
	}
	return out
}

func appendOutput(path, name, value string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = fmt.Fprintf(f, "%s=%s\n", name, value)
	return err
}

// concatenate concatenates on or after the concatenation day, and commits
// it, unless the record sets before and after it differ.
func (r *run) concatenate(ctx context.Context) (string, []check.Finding, error) {
	if err := store.AggregateWithPolicy(r.dataDir, r.now, fetch.ConcatenationPolicy(r.cfg, r.dataDir, r.now)); err != nil {
		return "", nil, fmt.Errorf("concatenate: %w", err)
	}
	dirty, err := r.git.dirty(ctx, r.dataRel)
	if err != nil || !dirty {
		return "", nil, err
	}
	findings, err := check.RecordsMoved(ctx, r.dataDir, check.Diff{Base: "HEAD"})
	if err != nil {
		return "", nil, err
	}
	if len(findings) > 0 {
		// Concatenating later loses nothing; committing this would.
		if _, err := r.git.run(ctx, "checkout", "--", r.dataRel); err != nil {
			return "", nil, err
		}
		if _, err := r.git.run(ctx, "clean", "-fdq", "--", r.dataRel); err != nil {
			return "", nil, err
		}
		return "", findings, nil
	}
	if r.opts.BuildDB != nil {
		r.opts.BuildDB()
	}
	if _, err := r.git.run(ctx, "add", "--all", "--", r.veloRel); err != nil {
		return "", nil, err
	}
	added, err := r.git.run(ctx, "diff", "--cached", "--name-only", "--diff-filter=A")
	if err != nil {
		return "", nil, err
	}
	commit, err := r.git.commitPaths(ctx, concatenationMessage(strings.Split(added, "\n")), r.veloRel)
	return commit, nil, err
}

// wouldEnforce lists what an enforcing gate would have done with the
// night's findings: restore removed records, keep changed values, and hold
// back the new records of projects with other errors.
func wouldEnforce(night check.Report) (restore, keep, holdBack []string) {
	add := func(list *[]string, f check.Finding) {
		where := f.Source + "/" + f.Project
		for _, w := range *list {
			if w == where {
				return
			}
		}
		*list = append(*list, where)
	}
	for _, f := range night.Findings {
		if f.Accepted || f.Severity != check.Error || f.Source == "" || f.Project == "" {
			continue
		}
		switch f.Check {
		case "diff.records-removed", "diff.content-removed":
			add(&restore, f)
		case "diff.values-changed":
			add(&keep, f)
		case "fetch.failed":
		default:
			add(&holdBack, f)
		}
	}
	sort.Strings(restore)
	sort.Strings(keep)
	sort.Strings(holdBack)
	return restore, keep, holdBack
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// prepareHeldBack discards what the run was asked to discard, and overlays
// the rest. It returns the overlaid sources and projects.
func (r *run) prepareHeldBack() ([]string, []string, error) {
	var notes []string
	if r.opts.Discard != "" {
		if _, err := os.Stat(r.heldBackPath(r.opts.Discard)); err == nil {
			if err := os.RemoveAll(r.heldBackPath(r.opts.Discard)); err != nil {
				return nil, nil, err
			}
			notes = append(notes, "Discarded the held-back data of "+r.opts.Discard+"; it's fetched again where it can be.")
		} else {
			notes = append(notes, "Nothing is held back for "+r.opts.Discard+".")
		}
	}
	held, err := r.heldBackProjects()
	if err != nil {
		return nil, nil, err
	}
	for _, key := range held {
		if err := r.overlay(key); err != nil {
			return nil, nil, fmt.Errorf("overlay held-back %s: %w", key, err)
		}
	}
	return held, notes, nil
}

// enforce applies the rule to the night's changes and holds back the new
// records of the sources and projects with blocking errors. It returns
// what it held back.
func (r *run) enforce(ctx context.Context, night check.Report) ([]string, error) {
	diffs, repairs, err := check.CompareData(ctx, r.dataDir, check.Diff{Base: "HEAD"})
	if err != nil {
		return nil, err
	}
	if err := r.applyRule(diffs, repairs); err != nil {
		return nil, fmt.Errorf("restore stored data: %w", err)
	}
	keys, global := blocked(night.Findings)
	if global {
		for _, pd := range diffs {
			keys[pd.Source+"/"+pd.Project] = true
		}
	}
	delete(keys, r.opts.Release)
	changed := map[string]bool{}
	for _, pd := range diffs {
		changed[pd.Source+"/"+pd.Project] = true
	}
	var out []string
	for key := range keys {
		if changed[key] {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	for _, key := range out {
		if err := r.holdBack(ctx, key, diffs); err != nil {
			return nil, fmt.Errorf("hold back %s: %w", key, err)
		}
	}
	return out, nil
}

// landHeldBack removes the held-back directories of the projects that were
// overlaid and aren't held back again: their data is in the data directory
// now.
func (r *run) landHeldBack(overlaid, heldNow []string) error {
	for _, key := range overlaid {
		if contains(heldNow, key) {
			continue
		}
		if err := os.RemoveAll(r.heldBackPath(key)); err != nil {
			return err
		}
		src, _ := splitKey(key)
		if entries, err := os.ReadDir(filepath.Join(r.heldBackRoot(), src)); err == nil && len(entries) == 0 {
			_ = os.Remove(filepath.Join(r.heldBackRoot(), src))
		}
	}
	return nil
}

// heldBackNotes says, for the commit message, what's held back and why.
func heldBackNotes(keys []string, night check.Report, veloRel string) []string {
	var out []string
	for _, key := range keys {
		var checks []string
		for _, f := range night.Findings {
			if blocks(f) && (f.Source+"/"+f.Project == key || f.Project == "") && !contains(checks, f.Check) {
				checks = append(checks, f.Check)
			}
		}
		out = append(out, fmt.Sprintf("%s (%s), see %s/", key, strings.Join(checks, ", "), joinRel(veloRel, HeldBackDir+"/"+key)))
	}
	return out
}
