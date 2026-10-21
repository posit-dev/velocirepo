package fetch

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

// endDateSource records the end date the orchestrator asks it for.
type endDateSource struct {
	name string
	got  *time.Time
}

func (s endDateSource) Name() string { return s.name }

func (s endDateSource) Fetch(_ context.Context, opts source.FetchOptions) ([]source.Record, error) {
	*s.got = opts.EndDate
	return nil, nil
}

func TestFetchRejectsStartDateAfterEndDate(t *testing.T) {
	_, err := Source(context.Background(), pypiConfig(t.TempDir()), Tokens{}, "pypi",
		Options{StartDate: "2025-02-10", EndDate: "2025-02-01"},
		func(string, config.Project) []source.Source { return []source.Source{rangeSource{target: "pkg"}} },
	)
	if err == nil || !strings.Contains(err.Error(), "after end date") {
		t.Fatalf("err = %v, want start date after end date", err)
	}
}

// Plausible reports days in the site's time zone, so its default end date is
// one day earlier than other sources'. An explicit end date is respected.
func TestDefaultEndDateSettlesPlausible(t *testing.T) {
	now := time.Date(2025, 3, 10, 1, 0, 0, 0, time.UTC)
	tests := []struct {
		sourceName string
		endDate    string
		want       string
	}{
		{"plausible", "", "2025-03-08"},
		{"pypi", "", "2025-03-09"},
		{"plausible", "2025-03-09", "2025-03-09"},
	}
	for _, tt := range tests {
		var got time.Time
		cfg := &config.Config{Data: config.DataConfig{Dir: t.TempDir()}, Projects: map[string]config.Project{"proj": {}}}
		if _, err := Source(context.Background(), cfg, Tokens{}, tt.sourceName,
			Options{EndDate: tt.endDate, StartDate: "2025-03-01", Now: func() time.Time { return now }},
			func(string, config.Project) []source.Source {
				return []source.Source{endDateSource{name: tt.sourceName, got: &got}}
			},
		); err != nil {
			t.Fatal(err)
		}
		if got.Format("2006-01-02") != tt.want {
			t.Errorf("%s with end date %q fetched through %s, want %s", tt.sourceName, tt.endDate, got.Format("2006-01-02"), tt.want)
		}
	}
}

func TestTrafficGapIsLogged(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	now := time.Date(2025, 3, 20, 1, 0, 0, 0, time.UTC)
	dataDir := t.TempDir()
	if _, err := resolveStartDate(dataDir, "github-traffic", "proj", "", now); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Errorf("first fetch logged a gap: %s", logs.String())
	}

	if _, err := Source(context.Background(), &config.Config{Data: config.DataConfig{Dir: dataDir}, Projects: map[string]config.Project{"proj": {}}}, Tokens{}, "github-traffic",
		Options{StartDate: "2025-02-01", EndDate: "2025-02-01", NoConcatenate: true},
		func(string, config.Project) []source.Source { return []source.Source{trafficDay{}} },
	); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveStartDate(dataDir, "github-traffic", "proj", "", now); err != nil {
		t.Fatal(err)
	}
	if out := logs.String(); !strings.Contains(out, "14 days") || !strings.Contains(out, "from=2025-02-02") || !strings.Contains(out, "to=2025-03-05") {
		t.Errorf("log = %q, want a warning for 2025-02-02..2025-03-05", out)
	}
}

// trafficDay returns one traffic record for the start date.
type trafficDay struct{}

func (trafficDay) Name() string { return "github-traffic" }

func (trafficDay) Fetch(_ context.Context, opts source.FetchOptions) ([]source.Record, error) {
	return []source.Record{{Metric: "daily_views", ProjectID: opts.ProjectID, Target: "org/repo", Date: opts.StartDate.Format("2006-01-02"), Value: 1}}, nil
}

// fakeEvents is an event source that returns the given events, or fails.
type fakeEvents struct {
	events []source.Event
	err    error
}

func (fakeEvents) Name() string { return "github" }

func (s fakeEvents) FetchEvents(_ context.Context, _ source.FetchOptions) ([]source.Event, error) {
	return s.events, s.err
}

func eventJob(targets []string, sources ...source.EventSource) []fetchJob {
	return []fetchJob{{sourceName: "github", projectID: "proj", eventSources: sources, eventTargets: targets}}
}

// A repository without activity has no events to show how far it was
// fetched. A successful run records it, so the next run resumes after it.
func TestEventJobRecordsFetchedDateWithoutEvents(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{Data: config.DataConfig{Dir: dataDir}}
	if _, err := runJobs(context.Background(), cfg, Options{StartDate: "2025-03-01", EndDate: "2025-03-09"},
		eventJob([]string{"org/repo"}, fakeEvents{}), 1); err != nil {
		t.Fatal(err)
	}

	start, err := resolveStartDate(dataDir, "github", "proj", "", time.Date(2025, 3, 11, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if start.Format("2006-01-02") != "2025-03-10" {
		t.Errorf("next run starts at %s, want 2025-03-10", start.Format("2006-01-02"))
	}
}

// When one repository of a project fails, nothing is written for the
// project and the fetched date doesn't move, so the next run retries all of
// it.
func TestEventJobWritesNothingWhenARepositoryFails(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{Data: config.DataConfig{Dir: dataDir}}
	ok := fakeEvents{events: []source.Event{{Type: "star", ProjectID: "proj", Target: "org/a", Datetime: "2025-03-05T10:00:00Z", User: "alice"}}}
	failing := fakeEvents{err: errors.New("boom")}

	results, err := runJobs(context.Background(), cfg, Options{StartDate: "2025-03-01", EndDate: "2025-03-09"},
		eventJob([]string{"org/a", "org/b"}, ok, failing), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Error != "boom" {
		t.Fatalf("results = %+v, want one error result", results)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "events")); !os.IsNotExist(err) {
		t.Errorf("events were written despite the failure")
	}
	if last, err := store.LastDate(dataDir, "github", "proj"); err != nil || !last.IsZero() {
		t.Errorf("LastDate = %v (%v), want nothing recorded", last, err)
	}
}

// fullEvents is an event source that also provides content and records, like
// the GitHub source.
type fullEvents struct{ fakeEvents }

func (fullEvents) ContentByFilename() map[string][]source.ContentEntry {
	return map[string][]source.ContentEntry{
		"issues.jsonl": {{Source: "github", Target: "org/repo", ID: "issue/1", Title: "Bug", PublishedAt: "2025-03-05T09:00:00Z"}},
	}
}

func (fullEvents) Records() []source.Record {
	return []source.Record{{Metric: "total_reactions", ProjectID: "proj", Target: "org/repo", Date: "2025-03-09", Value: 2, Extra: map[string]string{"ref": "1"}}}
}

func TestEventJobWritesEventsContentAndRecords(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{Data: config.DataConfig{Dir: dataDir}}
	src := fullEvents{fakeEvents{events: []source.Event{{Type: "issue_open", ProjectID: "proj", Target: "org/repo", Datetime: "2025-03-05T09:00:00Z", User: "alice"}}}}

	results, err := runJobs(context.Background(), cfg, Options{StartDate: "2025-03-01", EndDate: "2025-03-09", NoConcatenate: true}, eventJob([]string{"org/repo"}, src), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Error != "" || results[0].Records != 2 || results[0].Files != 3 {
		t.Fatalf("results = %+v, want one success with 2 records in 3 files", results)
	}
	if events, err := store.ReadEvents(filepath.Join(store.EventsProjectDir(dataDir, "github", "proj"), "2025-03-05.jsonl")); err != nil || len(events) != 1 {
		t.Errorf("events = %v (%v), want the issue_open event", events, err)
	}
	if content, err := store.ReadContent(filepath.Join(store.ContentProjectDir(dataDir, "github", "proj"), "issues.jsonl")); err != nil || len(content) != 1 || content[0].ProjectID != "proj" {
		t.Errorf("content = %+v (%v), want issue/1 stamped with the project", content, err)
	}
	if records, err := store.ReadRecords(filepath.Join(store.MetricsProjectDir(dataDir, "github", "proj"), "2025-03-09.jsonl")); err != nil || len(records) != 1 || records[0].Value != 2 {
		t.Errorf("records = %+v (%v), want total_reactions 2", records, err)
	}
	if last, err := store.LastDate(dataDir, "github", "proj"); err != nil || last.Format("2006-01-02") != "2025-03-09" {
		t.Errorf("LastDate = %v (%v), want 2025-03-09", last, err)
	}
}

// optsEvents records the options the orchestrator passes to an event source.
type optsEvents struct{ got *source.FetchOptions }

func (optsEvents) Name() string { return "github" }

func (s optsEvents) FetchEvents(_ context.Context, opts source.FetchOptions) ([]source.Event, error) {
	*s.got = opts
	return nil, nil
}

// Repositories without stored daily stars get their whole star history read
// once; the others only the fetch window.
func TestEventJobBackfillsStarHistoryOfNewRepositories(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{Data: config.DataConfig{Dir: dataDir}}
	stored := []source.Record{{Metric: "daily_stars", ProjectID: "proj", Target: "org/a", Date: "2025-02-01", Value: 1}}
	if _, err := store.WriteRecords(dataDir, "github", "proj", stored); err != nil {
		t.Fatal(err)
	}

	var got source.FetchOptions
	if _, err := runJobs(context.Background(), cfg, Options{StartDate: "2025-03-01", EndDate: "2025-03-09"},
		eventJob([]string{"org/a", "org/b"}, optsEvents{&got}), 1); err != nil {
		t.Fatal(err)
	}
	if w := got.Window("stars"); !w.Start.IsZero() {
		t.Errorf("star window = %+v, want the whole history", w)
	}
}
