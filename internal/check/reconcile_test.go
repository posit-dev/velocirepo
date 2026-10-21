package check

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/fetch"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

// cannedReconciler returns fixed reference totals.
type cannedReconciler struct {
	totals []source.ReferenceTotal
	err    error
}

func (c cannedReconciler) ReferenceTotals(context.Context, []string) ([]source.ReferenceTotal, error) {
	return c.totals, c.err
}

func issues(target string, value, tolerance int64) source.ReferenceTotal {
	return source.ReferenceTotal{Target: target, Kind: "events", Metric: "issue_open", Label: "issues", Value: value, Tolerance: tolerance}
}

func stars(target string, value, tolerance int64) source.ReferenceTotal {
	return source.ReferenceTotal{Target: target, Kind: "records", Metric: "daily_stars", Label: "stars", Value: value, Tolerance: tolerance}
}

// storeHistory stores n issues and a star count for a repository.
func storeHistory(t *testing.T, dataDir, project, repo string, n int, starCount int64) {
	t.Helper()
	var events []source.Event
	for i := 0; i < n; i++ {
		ref := i + 1
		events = append(events, source.Event{Type: "issue_open", ProjectID: project, Target: repo, Datetime: fmt.Sprintf("2026-10-%02dT%02d:%02d:00Z", 1+i%28, i/60%24, i%60), Ref: &ref, User: "alice"})
	}
	if len(events) > 0 {
		if _, err := store.WriteEvents(dataDir, "github", project, events); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.WriteRecords(dataDir, "github", project, []source.Record{{Metric: "daily_stars", ProjectID: project, Target: repo, Date: "2026-10-01", Value: starCount}}); err != nil {
		t.Fatal(err)
	}
}

func reconcileFindings(t *testing.T, dataDir string, rec source.Reconciler, results *fetch.RunResults) []Finding {
	t.Helper()
	cfg := &config.Config{Projects: map[string]config.Project{"positron": {GitHubEvents: config.StringList{"posit-dev/positron"}}}}
	fs, err := reconciliation{}.Run(context.Background(), &Context{Config: cfg, DataDir: dataDir, Now: now, Results: results, Reconcilers: map[string]source.Reconciler{"github": rec}})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestReconcileMatchingAndWithinTolerance(t *testing.T) {
	dataDir := t.TempDir()
	storeHistory(t, dataDir, "positron", "posit-dev/positron", 50, 300)
	rec := cannedReconciler{totals: []source.ReferenceTotal{issues("posit-dev/positron", 50, 5), stars("posit-dev/positron", 300, 5)}}
	if fs := reconcileFindings(t, dataDir, rec, nil); len(fs) != 0 {
		t.Errorf("matching totals reported %+v", fs)
	}
	// New issues since the last day fetched, and stars taken back.
	rec = cannedReconciler{totals: []source.ReferenceTotal{issues("posit-dev/positron", 53, 5), stars("posit-dev/positron", 296, 5)}}
	if fs := reconcileFindings(t, dataDir, rec, nil); len(fs) != 0 {
		t.Errorf("differences within tolerance reported %+v", fs)
	}
}

// Missing and duplicated data are both reported, with both numbers.
func TestReconcileBeyondTolerance(t *testing.T) {
	dataDir := t.TempDir()
	storeHistory(t, dataDir, "positron", "posit-dev/positron", 50, 600)
	rec := cannedReconciler{totals: []source.ReferenceTotal{issues("posit-dev/positron", 80, 5), stars("posit-dev/positron", 300, 5)}}
	fs := reconcileFindings(t, dataDir, rec, nil)
	if len(fs) != 2 {
		t.Fatalf("findings = %+v", fs)
	}
	for _, f := range fs {
		if f.Check != "reconcile.totals" || f.Severity != Warning {
			t.Errorf("finding = %+v", f)
		}
	}
	if !strings.Contains(fs[0].Message, "80 issues, against 50 stored (missing") {
		t.Errorf("message = %q", fs[0].Message)
	}
	if !strings.Contains(fs[1].Message, "300 stars, against 600 stored (duplicated") {
		t.Errorf("message = %q", fs[1].Message)
	}
}

// The positron case: no stored events for a repository with thousands.
func TestReconcileMissingHistory(t *testing.T) {
	dataDir := t.TempDir()
	storeHistory(t, dataDir, "positron", "posit-dev/positron", 0, 10)
	rec := cannedReconciler{totals: []source.ReferenceTotal{issues("posit-dev/positron", 8172, 81)}}
	f := requireOnly(t, reconcileFindings(t, dataDir, rec, nil), "reconcile.totals", Warning)
	if !strings.Contains(f.Message, "8,172 issues, against 0 stored") {
		t.Errorf("message = %q", f.Message)
	}
}

func TestReconcileUnavailableIsNeverAMatch(t *testing.T) {
	dataDir := t.TempDir()
	storeHistory(t, dataDir, "positron", "posit-dev/positron", 5, 10)
	f := requireOnly(t, reconcileFindings(t, dataDir, cannedReconciler{err: errors.New("graphql returned 502")}, nil), "reconcile.unavailable", Warning)
	if !strings.Contains(f.Message, "502") {
		t.Errorf("message = %q", f.Message)
	}
}

func TestReconcileSuddenlyZeroReference(t *testing.T) {
	dataDir := t.TempDir()
	storeHistory(t, dataDir, "positron", "posit-dev/positron", 150, 10)
	rec := cannedReconciler{totals: []source.ReferenceTotal{issues("posit-dev/positron", 0, 5)}}
	requireOnly(t, reconcileFindings(t, dataDir, rec, nil), "reconcile.suspicious-reference", Warning)
}

func TestReconcileSkipsWhenTheQuotaIsLow(t *testing.T) {
	dataDir := t.TempDir()
	rec := cannedReconciler{err: errors.New("must not be asked")}
	results := &fetch.RunResults{Quota: map[string]fetch.Quota{"api.github.com/graphql": {Limit: 5000, Remaining: 300}}}
	requireOnly(t, reconcileFindings(t, dataDir, rec, results), "quota.low", Warning)
}
