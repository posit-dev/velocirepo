package fetch

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/posit-dev/velocirepo/internal/dateutil"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

// dayEndMargin is how long after a day has ended in its time zone it's
// fetched, so the source has finished counting it.
const dayEndMargin = time.Hour

// beginning is where a target without stored data starts.
var beginning = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// latestEndedDay returns the latest day that has ended in loc, at least
// dayEndMargin before now.
func latestEndedDay(now time.Time, loc *time.Location) time.Time {
	y, m, d := now.Add(-dayEndMargin).In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
}

// planner decides, per target and series, which days a run fetches.
type planner struct {
	dataDir string
	now     time.Time
	// start is an explicit start date, or zero to resume.
	start time.Time
	// end is an explicit end date, or zero for the latest day that has
	// ended.
	end time.Time
	// repair re-fetches the explicit range and replaces what's stored.
	repair bool

	mu        sync.Mutex
	published map[string]publishedDay
}

type publishedDay struct {
	day   time.Time
	known bool
}

// endDate returns the last day a series in loc can be fetched through: the
// latest day that has ended there, or an earlier explicit end date. An
// explicit end date that hasn't ended yet is moved back, since a day that's
// still in progress would be stored partially for good.
func (p *planner) endDate(loc *time.Location, sourceName, target string) time.Time {
	ended := latestEndedDay(p.now, loc)
	if p.end.IsZero() {
		return ended
	}
	if p.end.After(ended) {
		slog.Warn("end date hasn't ended yet in the target's time zone; fetching through the latest day that has",
			"source", sourceName, "target", target, "end_date", dateutil.FormatDate(p.end), "through", dateutil.FormatDate(ended))
		return ended
	}
	return p.end
}

// startDate returns the first day to fetch for a target and series: an
// explicit start date, or the day after its watermark. A target without a
// watermark gets one from its stored data, without a migration: the star
// history resumes after its latest stored day, other series where the
// project's data left off. A target without stored data starts from the
// beginning; for the star history, that's its whole history (a zero start).
func (p *planner) startDate(sourceName, projectID, target, series string, watermarks map[string]string) (time.Time, error) {
	if !p.start.IsZero() {
		return p.start, nil
	}
	if target == "" {
		// Sources without a target, such as custom sources in tests,
		// resume per project, as before.
		return resolveStartDate(p.dataDir, sourceName, projectID, "", p.now)
	}
	if w := watermarks[target]; w != "" {
		t, err := dateutil.ParseDate(w)
		if err != nil {
			return time.Time{}, err
		}
		return clampTraffic(sourceName, projectID, t, t.AddDate(0, 0, 1), p.now), nil
	}

	if series == "stars" {
		latest, err := store.LatestMetricDates(p.dataDir, sourceName, projectID, "daily_stars")
		if err != nil {
			return time.Time{}, err
		}
		if d := latest[target]; d != "" {
			t, err := dateutil.ParseDate(d)
			if err != nil {
				return time.Time{}, err
			}
			return t.AddDate(0, 0, 1), nil
		}
		return time.Time{}, nil
	}

	has, err := store.TargetHasData(p.dataDir, sourceName, projectID, target)
	if err != nil {
		return time.Time{}, err
	}
	if has {
		return resolveStartDate(p.dataDir, sourceName, projectID, "", p.now)
	}
	return clampTraffic(sourceName, projectID, time.Time{}, beginning, p.now), nil
}

// clampTraffic moves the start of GitHub traffic into the 14 days its API
// keeps. last is the day fetched last, or zero.
func clampTraffic(sourceName, projectID string, last, start, now time.Time) time.Time {
	if sourceName != "github-traffic" {
		return start
	}
	earliest := now.AddDate(0, 0, -14).Truncate(24 * time.Hour)
	if !start.Before(earliest) {
		return start
	}
	if !last.IsZero() {
		slog.Warn("github traffic only covers the last 14 days; earlier days can no longer be fetched",
			"project", projectID, "from", dateutil.FormatDate(start), "to", dateutil.FormatDate(earliest.AddDate(0, 0, -1)))
	}
	return earliest
}

// publishedThrough returns how far a source has published, asked once per
// source and run.
func (p *planner) publishedThrough(ctx context.Context, sourceName string, src any) publishedDay {
	pub, ok := src.(source.Publisher)
	if !ok {
		return publishedDay{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.published == nil {
		p.published = make(map[string]publishedDay)
	}
	if d, ok := p.published[sourceName]; ok {
		return d
	}
	day, err := pub.PublishedThrough(ctx)
	d := publishedDay{day: day, known: err == nil}
	if err != nil {
		slog.Warn("couldn't tell how far the source has published; watermarks only move to the latest day returned",
			"source", sourceName, "error", err)
	}
	p.published[sourceName] = d
	return d
}

// dayZone returns the time zone a source's series ends its days in.
func dayZone(ctx context.Context, src any, series string) (*time.Location, error) {
	if z, ok := src.(source.DayZoner); ok {
		return z.DayZone(ctx, series)
	}
	return time.UTC, nil
}

// latestRecordDate returns the latest date of a target's records, or zero.
func latestRecordDate(records []source.Record, target string) time.Time {
	latest := ""
	for _, r := range records {
		if r.Target == target && r.Date > latest {
			latest = r.Date
		}
	}
	if latest == "" {
		return time.Time{}
	}
	t, _ := dateutil.ParseDate(latest)
	return t
}
