package fetch

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

// zonedSource is a metric source whose days end in a time zone, and that
// records the days it was asked for. With a published day, it's a source
// that publishes late, like CRAN.
type zonedSource struct {
	target    string
	zone      *time.Location
	published time.Time
	pubErr    error
	quiet     bool
	mu        *sync.Mutex
	asked     *[]string
}

func (zonedSource) Name() string { return "pypi" }

func (s zonedSource) DayZone(context.Context, string) (*time.Location, error) {
	if s.zone == nil {
		return time.UTC, nil
	}
	return s.zone, nil
}

func (s zonedSource) Fetch(_ context.Context, opts source.FetchOptions) ([]source.Record, error) {
	var records []source.Record
	for d := opts.StartDate; !d.After(opts.EndDate); d = d.AddDate(0, 0, 1) {
		s.mu.Lock()
		*s.asked = append(*s.asked, s.target+" "+d.Format("2006-01-02"))
		s.mu.Unlock()
		if s.quiet || (!s.published.IsZero() && d.After(s.published)) {
			continue
		}
		records = append(records, source.Record{Metric: "daily_downloads", ProjectID: opts.ProjectID, Target: s.target, Date: d.Format("2006-01-02"), Value: 1})
	}
	return records, nil
}

// publishingSource is a zonedSource that reports its published day.
type publishingSource struct{ zonedSource }

func (s publishingSource) PublishedThrough(context.Context) (time.Time, error) {
	return s.published, s.pubErr
}

func metricJob(sources ...source.Source) []fetchJob {
	job := fetchJob{sourceName: "pypi", projectID: "proj"}
	for _, src := range sources {
		job.sources = append(job.sources, src)
		switch s := src.(type) {
		case zonedSource:
			job.targets = append(job.targets, s.target)
		case publishingSource:
			job.targets = append(job.targets, s.target)
		}
	}
	return []fetchJob{job}
}

func at(day, hour, minute int) func() time.Time {
	return func() time.Time { return time.Date(2026, 10, day, hour, minute, 0, 0, time.UTC) }
}

func newLog() (*[]string, *sync.Mutex) { return &[]string{}, &sync.Mutex{} }

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func mustRun(t *testing.T, cfg *config.Config, now func() time.Time, jobs []fetchJob) []Result {
	t.Helper()
	results, err := runJobs(context.Background(), cfg, Options{Now: now, NoConcatenate: true}, jobs, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Error != "" {
			t.Fatalf("job failed: %+v", r)
		}
	}
	return results
}

func seed(t *testing.T, dataDir, target, date string) {
	t.Helper()
	if err := store.MarkFetchedSeries(dataDir, "pypi", "proj", "", date, []string{target}); err != nil {
		t.Fatal(err)
	}
}

// A day is fetched once it has ended in the target's time zone, plus an
// hour. At 06:00 UTC, yesterday is over in UTC but not in Los Angeles; at
// 09:30 it's over in both. Between the two runs no day is skipped, and none
// is fetched twice.
func TestDaysAreFetchedOnceTheyHaveEnded(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	dataDir := t.TempDir()
	cfg := &config.Config{Data: config.DataConfig{Dir: dataDir}}
	seed(t, dataDir, "utc", "2026-10-18")
	seed(t, dataDir, "la", "2026-10-18")

	asked, mu := newLog()
	jobs := func() []fetchJob {
		return metricJob(zonedSource{target: "utc", mu: mu, asked: asked}, zonedSource{target: "la", zone: la, mu: mu, asked: asked})
	}

	mustRun(t, cfg, at(21, 6, 0), jobs())
	if got, want := sortedCopy(*asked), []string{"la 2026-10-19", "utc 2026-10-19", "utc 2026-10-20"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("06:00 run fetched %v, want %v", got, want)
	}

	*asked = nil
	mustRun(t, cfg, at(21, 9, 30), jobs())
	if got, want := sortedCopy(*asked), []string{"la 2026-10-20"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("09:30 run fetched %v, want %v", got, want)
	}
}

// Daylight saving time: in winter, Los Angeles is at UTC-8, so its yesterday
// ends at 08:00 UTC, plus the margin.
func TestDayEndsInWinter(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	if got := latestEndedDay(time.Date(2026, 12, 10, 8, 59, 0, 0, time.UTC), la); got.Format("2006-01-02") != "2026-12-08" {
		t.Errorf("at 08:59 UTC in winter, LA's latest ended day = %s, want 2026-12-08", got.Format("2006-01-02"))
	}
	if got := latestEndedDay(time.Date(2026, 12, 10, 9, 0, 0, 0, time.UTC), la); got.Format("2006-01-02") != "2026-12-09" {
		t.Errorf("at 09:00 UTC in winter, LA's latest ended day = %s, want 2026-12-09", got.Format("2006-01-02"))
	}
}

// CRAN publishes two days late. Its watermark stops at the published day, so
// the next run fetches the day that wasn't published yet.
func TestWatermarksStopAtThePublishedDay(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{Data: config.DataConfig{Dir: dataDir}}
	seed(t, dataDir, "dplyr", "2026-10-18")

	asked, mu := newLog()
	src := publishingSource{zonedSource{target: "dplyr", published: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), mu: mu, asked: asked}}
	mustRun(t, cfg, at(21, 9, 30), metricJob(src))
	marks, _ := store.SeriesWatermarks(dataDir, "pypi", "proj", "")
	if marks["dplyr"] != "2026-10-19" {
		t.Errorf("watermark = %s, want the published day 2026-10-19", marks["dplyr"])
	}
	if got := strings.Join(*asked, ","); got != "dplyr 2026-10-19" {
		t.Errorf("fetched %s, want only the published day", got)
	}

	*asked = nil
	src.published = time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	mustRun(t, cfg, at(22, 9, 30), metricJob(src))
	if got := strings.Join(*asked, ","); got != "dplyr 2026-10-20,dplyr 2026-10-21" {
		t.Errorf("next run fetched %s, want 2026-10-20 and 21", got)
	}
}

// A source that can't tell how far it has published only moves its
// watermark to the latest day it returned.
func TestUnknownPublishedDayMovesToTheLatestDayReturned(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{Data: config.DataConfig{Dir: dataDir}}
	seed(t, dataDir, "dplyr", "2026-10-18")
	asked, mu := newLog()
	src := publishingSource{zonedSource{target: "dplyr", published: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), pubErr: context.DeadlineExceeded, mu: mu, asked: asked}}
	mustRun(t, cfg, at(21, 9, 30), metricJob(src))
	marks, _ := store.SeriesWatermarks(dataDir, "pypi", "proj", "")
	if marks["dplyr"] != "2026-10-19" {
		t.Errorf("watermark = %s, want the latest day returned, 2026-10-19", marks["dplyr"])
	}
}

// A real-time source without data on a day (a quiet package) still moves its
// watermark, and stores nothing.
func TestQuietTargetsMoveTheirWatermark(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{Data: config.DataConfig{Dir: dataDir}}
	seed(t, dataDir, "quiet", "2026-10-18")
	asked, mu := newLog()
	results := mustRun(t, cfg, at(21, 9, 30), metricJob(zonedSource{target: "quiet", quiet: true, mu: mu, asked: asked}))
	marks, _ := store.SeriesWatermarks(dataDir, "pypi", "proj", "")
	if marks["quiet"] != "2026-10-20" {
		t.Errorf("watermark = %s, want 2026-10-20", marks["quiet"])
	}
	if len(results) != 1 || results[0].Skipped != "no new records" {
		t.Errorf("results = %+v", results)
	}
}

// An explicit end date that hasn't ended yet is moved back, with a warning,
// since a day in progress would be stored partially for good.
func TestExplicitEndDateIsClamped(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	dataDir := t.TempDir()
	cfg := &config.Config{Data: config.DataConfig{Dir: dataDir}}
	asked, mu := newLog()
	if _, err := runJobs(context.Background(), cfg, Options{Now: at(21, 9, 30), StartDate: "2026-10-20", EndDate: "2026-10-21", NoConcatenate: true},
		metricJob(zonedSource{target: "pkg", mu: mu, asked: asked}), 1); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*asked, ","); got != "pkg 2026-10-20" {
		t.Errorf("fetched %s, want only 2026-10-20", got)
	}
	if !strings.Contains(logs.String(), "hasn't ended yet") {
		t.Errorf("no warning: %s", logs.String())
	}
}

// Upgrading needs no migration: a target with stored data but no watermark
// resumes where its project's data left off, and a target without any data
// starts from the beginning.
func TestWatermarksAreSeededFromStoredData(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{Data: config.DataConfig{Dir: dataDir}}
	if _, err := store.WriteRecords(dataDir, "pypi", "proj", []source.Record{
		{Metric: "daily_downloads", ProjectID: "proj", Target: "old", Date: "2026-10-19", Value: 1},
	}); err != nil {
		t.Fatal(err)
	}
	asked, mu := newLog()
	mustRun(t, cfg, at(21, 9, 30), metricJob(zonedSource{target: "old", mu: mu, asked: asked}, zonedSource{target: "new", quiet: true, mu: mu, asked: asked}))

	var oldDays, newDays int
	for _, a := range *asked {
		if strings.HasPrefix(a, "old ") {
			oldDays++
		} else {
			newDays++
		}
	}
	if oldDays != 1 {
		t.Errorf("the existing target fetched %d days, want 1 (2026-10-20)", oldDays)
	}
	if !strings.Contains(strings.Join(*asked, ","), "new 2000-01-01") {
		t.Errorf("the new target didn't start from the beginning (%d days)", newDays)
	}
}

// zonedEvents is an event source with GitHub's time zones that records its
// windows and returns one comment per day of its events window.
type zonedEvents struct {
	target string
	mu     *sync.Mutex
	asked  *[]string
}

func (zonedEvents) Name() string { return "github" }

func (s zonedEvents) DayZone(_ context.Context, series string) (*time.Location, error) {
	if series == "stars" {
		return time.LoadLocation("America/Los_Angeles")
	}
	return time.UTC, nil
}

func (s zonedEvents) FetchEvents(_ context.Context, opts source.FetchOptions) ([]source.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var events []source.Event
	if w := opts.Window(""); !w.Empty() {
		for d := w.Start; !d.After(w.End); d = d.AddDate(0, 0, 1) {
			*s.asked = append(*s.asked, s.target+" events "+d.Format("2006-01-02"))
			events = append(events, source.Event{Type: "comment", ProjectID: opts.ProjectID, Target: s.target, Datetime: d.Format("2006-01-02") + "T12:00:00Z", User: "u"})
		}
	}
	if w := opts.Window("stars"); !w.Empty() {
		start := w.Start
		if start.IsZero() {
			*s.asked = append(*s.asked, s.target+" stars from the beginning")
			start = w.End
		}
		for d := start; !d.After(w.End); d = d.AddDate(0, 0, 1) {
			*s.asked = append(*s.asked, s.target+" stars "+d.Format("2006-01-02"))
		}
	}
	return events, nil
}

// The star history ends its days in US Pacific time, so it has its own
// watermark. An early run fetches the events through yesterday and the stars
// through the day before; the next run catches the stars up, and no star day
// is missed.
func TestStarHistoryHasItsOwnResumePoint(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{Data: config.DataConfig{Dir: dataDir}}
	if err := store.MarkFetchedSeries(dataDir, "github", "proj", "", "2026-10-18", []string{"org/a"}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkFetchedSeries(dataDir, "github", "proj", "stars", "2026-10-18", []string{"org/a"}); err != nil {
		t.Fatal(err)
	}
	asked, mu := newLog()
	job := func() []fetchJob {
		return eventJob([]string{"org/a"}, zonedEvents{target: "org/a", mu: mu, asked: asked})
	}

	mustRun(t, cfg, at(21, 6, 0), job())
	if got, want := sortedCopy(*asked), []string{"org/a events 2026-10-19", "org/a events 2026-10-20", "org/a stars 2026-10-19"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("06:00 run fetched %v, want %v", got, want)
	}
	*asked = nil
	mustRun(t, cfg, at(21, 9, 30), job())
	if got, want := sortedCopy(*asked), []string{"org/a stars 2026-10-20"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("09:30 run fetched %v, want %v", got, want)
	}
}

// A repository added to a project is fetched from the beginning, with its
// whole star history, while the other repositories only fetch yesterday. The
// other repositories' stored events stay: each write only replaces the days
// in its own target's window.
func TestNewRepositoryKeepsTheOthersEvents(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{Data: config.DataConfig{Dir: dataDir}}
	if _, err := store.WriteEvents(dataDir, "github", "proj", []source.Event{
		{Type: "comment", ProjectID: "proj", Target: "org/a", Datetime: "2026-10-15T09:00:00Z", User: "alice"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, series := range []string{"", "stars"} {
		if err := store.MarkFetchedSeries(dataDir, "github", "proj", series, "2026-10-19", []string{"org/a"}); err != nil {
			t.Fatal(err)
		}
	}

	asked, mu := newLog()
	mustRun(t, cfg, at(21, 9, 30), eventJob([]string{"org/a", "org/new"},
		zonedEvents{target: "org/a", mu: mu, asked: asked}, zonedEvents{target: "org/new", mu: mu, asked: asked}))

	if !strings.Contains(strings.Join(*asked, ","), "org/new stars from the beginning") {
		t.Errorf("the new repository's star history wasn't fetched from the beginning: %v", *asked)
	}
	events, err := store.ReadEvents(store.EventsProjectDir(dataDir, "github", "proj") + "/2026-10-15.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var targets []string
	for _, e := range events {
		targets = append(targets, e.Target+":"+e.User)
	}
	if got := strings.Join(sortedCopy(targets), ","); got != "org/a:alice,org/new:u" {
		t.Errorf("2026-10-15 = %s, want org/a's comment kept next to org/new's", got)
	}
}

// A repair replaces what's stored for its days, also with "no data", and
// leaves the days outside it and the watermarks as they are.
func TestRepairReplacesItsRange(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		dataDir := t.TempDir()
		var stored []source.Record
		for d := 1; d <= 6; d++ {
			stored = append(stored, source.Record{Metric: "daily_downloads", ProjectID: "proj", Target: "pkg", Date: fmt.Sprintf("2026-10-%02d", d), Value: 9})
		}
		if _, err := store.WriteRecords(dataDir, "pypi", "proj", stored); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkFetchedSeries(dataDir, "pypi", "proj", "", "2026-10-06", []string{"pkg"}); err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		var asked []string
		src := zonedSource{target: "pkg", quiet: quiet, mu: &mu, asked: &asked}
		cfg := &config.Config{Data: config.DataConfig{Dir: dataDir}}
		opts := Options{Now: at(21, 9, 30), NoConcatenate: true, Repair: true, StartDate: "2026-10-02", EndDate: "2026-10-04"}
		job := fetchJob{sourceName: "pypi", projectID: "proj", targets: []string{"pkg"}, sources: []source.Source{src}}
		if _, err := runJobs(context.Background(), cfg, opts, []fetchJob{job}, 1); err != nil {
			t.Fatal(err)
		}
		got := map[string]int64{}
		paths, _ := filepath.Glob(filepath.Join(store.MetricsProjectDir(dataDir, "pypi", "proj"), "*.jsonl"))
		for _, p := range paths {
			records, _ := store.ReadRecords(p)
			for _, r := range records {
				got[r.Date] = r.Value
			}
		}
		want := map[string]int64{"2026-10-01": 9, "2026-10-02": 1, "2026-10-03": 1, "2026-10-04": 1, "2026-10-05": 9, "2026-10-06": 9}
		if quiet {
			want = map[string]int64{"2026-10-01": 9, "2026-10-05": 9, "2026-10-06": 9}
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("quiet=%v: stored %v, want %v", quiet, got, want)
		}
		if marks, _ := store.SeriesWatermarks(dataDir, "pypi", "proj", ""); marks["pkg"] != "2026-10-06" {
			t.Errorf("the watermark moved: %v", marks)
		}
	}
}
