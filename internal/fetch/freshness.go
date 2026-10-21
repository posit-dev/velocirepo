package fetch

import (
	"sort"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/dateutil"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

// TargetFreshness is how far a configured target's series has been fetched.
type TargetFreshness struct {
	Source  string
	Project string
	Target  string
	Series  string
	// Watermark is the last day fetched, or "" if the target has no
	// watermark yet.
	Watermark string
	// Limit is the most days the watermark may lag behind today (UTC); a
	// watermark of yesterday is one day behind.
	Limit int
	// Lag is how many days the watermark lags behind today (UTC).
	Lag int
}

// Stale reports whether the target is further behind than its limit.
func (f TargetFreshness) Stale() bool {
	return f.Watermark != "" && f.Lag > f.Limit
}

// Freshness returns the freshness of every target and series in the config,
// sorted by source, project, target and series. Sources without a freshness
// limit are left out.
func Freshness(cfg *config.Config, dataDir string, now time.Time) ([]TargetFreshness, error) {
	y, m, d := now.UTC().Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	var out []TargetFreshness
	for _, desc := range fetchSourceDescriptors {
		meta, ok := source.MetaFor(desc.Name)
		if !ok || len(meta.Freshness) == 0 {
			continue
		}
		series := make([]string, 0, len(meta.Freshness))
		for s := range meta.Freshness {
			series = append(series, s)
		}
		sort.Strings(series)
		for projectID, proj := range cfg.Projects {
			targets := proj.SourceValues(desc.Name)
			if len(targets) == 0 {
				continue
			}
			for _, s := range series {
				marks, err := store.SeriesWatermarks(dataDir, desc.Name, projectID, s)
				if err != nil {
					return nil, err
				}
				for _, target := range targets {
					f := TargetFreshness{Source: desc.Name, Project: projectID, Target: target, Series: s, Watermark: marks[target], Limit: meta.Freshness[s]}
					if f.Watermark != "" {
						t, err := dateutil.ParseDate(f.Watermark)
						if err != nil {
							return nil, err
						}
						f.Lag = max(0, int(today.Sub(t).Hours()/24))
					}
					out = append(out, f)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		return a.Series < b.Series
	})
	return out, nil
}

// ConcatenationPolicy returns when fetch concatenates: on or after the
// configured day of the following month, once every target and series of the
// project in the config has been fetched past the period's last day, or is
// further behind than its freshness limit, so a broken target doesn't hold
// the others up for good. Targets removed from the config don't count. A
// project with a target without a watermark, or that isn't in the config,
// falls back to its stored data reaching the period's last day.
func ConcatenationPolicy(cfg *config.Config, dataDir string, now time.Time) store.Policy {
	byProject := map[[2]string][]TargetFreshness{}
	if all, err := Freshness(cfg, dataDir, now); err == nil {
		for _, f := range all {
			key := [2]string{f.Source, f.Project}
			byProject[key] = append(byProject[key], f)
		}
	}
	return store.Policy{
		Day: cfg.Settings.ConcatenationDay(),
		Ready: func(category, sourceName, projectID, lastDay string) bool {
			targets := byProject[[2]string{sourceName, projectID}]
			for _, f := range targets {
				if f.Watermark == "" {
					targets = nil
					break
				}
			}
			if len(targets) == 0 {
				through, err := store.StoredThrough(dataDir, category, sourceName, projectID)
				return err == nil && through >= lastDay
			}
			for _, f := range targets {
				if f.Watermark < lastDay && !f.Stale() {
					return false
				}
			}
			return true
		},
	}
}
