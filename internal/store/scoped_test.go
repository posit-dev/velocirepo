package store

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/source"
)

func day(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }

func ev(target, datetime, user string) source.Event {
	return source.Event{Type: "comment", ProjectID: "proj", Target: target, Datetime: datetime, User: user}
}

func eventUsers(t *testing.T, dataDir, date string) []string {
	t.Helper()
	events, err := ReadEvents(filepath.Join(EventsProjectDir(dataDir, "github", "proj"), date+".jsonl"))
	if err != nil {
		return nil
	}
	var users []string
	for _, e := range events {
		users = append(users, e.Target+":"+e.User)
	}
	sort.Strings(users)
	return users
}

// Adding a repository to a project fetches its whole history while the other
// repositories only fetch yesterday. A write may only replace the events of
// the targets and days it covers: before, a write replaced the whole day.
func TestScopedEventWritesKeepOtherTargets(t *testing.T) {
	dataDir := t.TempDir()
	if _, err := WriteEvents(dataDir, "github", "proj", []source.Event{
		ev("org/a", "2026-10-05T10:00:00Z", "alice"),
		ev("org/a", "2026-10-06T10:00:00Z", "deleted-later"),
	}); err != nil {
		t.Fatal(err)
	}

	// org/b is new and fetched from the beginning; org/a only on the 7th.
	_, err := WriteEventsScoped(dataDir, "github", "proj", []source.Event{
		ev("org/b", "2026-10-05T11:00:00Z", "bob"),
		ev("org/a", "2026-10-07T10:00:00Z", "carol"),
	}, map[string]source.Window{
		"org/a": {Start: day(7), End: day(7)},
		"org/b": {End: day(7)},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got, want := eventUsers(t, dataDir, "2026-10-05"), []string{"org/a:alice", "org/b:bob"}; !reflect.DeepEqual(got, want) {
		t.Errorf("2026-10-05 = %v, want %v", got, want)
	}
	if got, want := eventUsers(t, dataDir, "2026-10-06"), []string{"org/a:deleted-later"}; !reflect.DeepEqual(got, want) {
		t.Errorf("2026-10-06 = %v, want %v (outside org/a's window)", got, want)
	}

	// A target's window says what's true for those days: a comment that's
	// gone from GitHub is gone from the day.
	if _, err := WriteEventsScoped(dataDir, "github", "proj", nil, map[string]source.Window{
		"org/a": {Start: day(6), End: day(6)},
	}); err != nil {
		t.Fatal(err)
	}
	if got := eventUsers(t, dataDir, "2026-10-06"); len(got) != 0 {
		t.Errorf("2026-10-06 = %v, want the deleted comment gone", got)
	}
}

func TestTargetHasDataAndLatestMetricDates(t *testing.T) {
	dataDir := t.TempDir()
	if _, err := WriteEvents(dataDir, "github", "proj", []source.Event{ev("org/a", "2026-10-05T10:00:00Z", "alice")}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteRecords(dataDir, "github", "proj", []source.Record{
		{Metric: "daily_stars", ProjectID: "proj", Target: "org/c", Date: "2026-10-03", Value: 1},
		{Metric: "daily_stars", ProjectID: "proj", Target: "org/c", Date: "2026-10-09", Value: 2},
	}); err != nil {
		t.Fatal(err)
	}

	for target, want := range map[string]bool{"org/a": true, "org/c": true, "org/new": false} {
		if got, err := TargetHasData(dataDir, "github", "proj", target); err != nil || got != want {
			t.Errorf("TargetHasData(%s) = %v, %v; want %v", target, got, err, want)
		}
	}
	latest, err := LatestMetricDates(dataDir, "github", "proj", "daily_stars")
	if err != nil || latest["org/c"] != "2026-10-09" || len(latest) != 1 {
		t.Errorf("LatestMetricDates = %v, %v; want org/c at 2026-10-09", latest, err)
	}
}
