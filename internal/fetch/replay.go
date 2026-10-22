package fetch

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/posit-dev/velocirepo/internal/archive"
	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/dateutil"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

// ReplayOptions selects what a replay rebuilds.
type ReplayOptions struct {
	Source  string
	Project string
}

// snapshotFetcher is an event source whose archived requests form a unit of
// their own: GitHub's snapshots, replayed without any event requests.
type snapshotFetcher interface {
	FetchSnapshots(ctx context.Context, opts source.FetchOptions, refs []int) error
	source.RecordProvider
	source.MultiContentProvider
}

// replayTokens stand in for the tokens, which the archived requests don't
// contain: they're redacted, and only matched against the archive.
var replayTokens = Tokens{GitHub: "replay", Plausible: "replay", YouTube: "replay", LinkedIn: "replay"}

// Replay rebuilds the records of an archived run: each job runs with the
// options in the run's manifest, against the archived responses instead of
// the network, so it makes exactly the requests the original run made,
// whatever has been stored since. Records and content are written like
// fetched ones; watermarks don't move.
func Replay(ctx context.Context, cfg *config.Config, run archive.Run, opts ReplayOptions) ([]Result, error) {
	client := &http.Client{Transport: archive.NewReplay(run.Exchanges)}
	dataDir := cfg.DataDir()

	// A job none of whose requests were archived has nothing to replay.
	archived := map[[3]string]bool{}
	for _, e := range run.Exchanges {
		archived[[3]string{e.Source, e.Project, e.Target}] = true
	}

	var results []Result
	for _, job := range run.Manifest.Jobs {
		if (opts.Source != "" && job.Source != opts.Source) || (opts.Project != "" && job.Project != opts.Project) {
			continue
		}
		desc, ok := fetchSourceByName(job.Source)
		if !ok {
			return results, fmt.Errorf("unknown source %q in the manifest", job.Source)
		}
		if !archived[[3]string{job.Source, job.Project, job.Target}] {
			continue
		}
		started := time.Now()
		r := Result{Source: job.Source, ProjectID: job.Project, StartDate: job.Start[""], EndDate: job.End[""]}
		records, content, err := replayJob(ctx, client, cfg, desc, job)
		if err != nil {
			r.Error = err.Error()
			r.Duration = time.Since(started)
			results = append(results, r)
			continue
		}
		if len(records) > 0 {
			n, err := store.WriteRecords(dataDir, job.Source, job.Project, records)
			if err != nil {
				return results, fmt.Errorf("write %s/%s: %w", job.Source, job.Project, err)
			}
			r.Files += n
		}
		for filename, entries := range content {
			if err := store.WriteContent(dataDir, job.Source, job.Project, filename, entries); err != nil {
				slog.Warn("write content failed", "source", job.Source, "project", job.Project, "error", err)
				continue
			}
			r.Files++
		}
		r.Records = len(records)
		r.Duration = time.Since(started)
		results = append(results, r)
	}
	return results, nil
}

// jobOptions returns the fetch options a manifest's job ran with.
func jobOptions(job archive.Job) (source.FetchOptions, error) {
	parse := func(s string) (time.Time, error) {
		if s == "" {
			return time.Time{}, nil
		}
		return dateutil.ParseDate(s)
	}
	start, err := parse(job.Start[""])
	if err != nil {
		return source.FetchOptions{}, err
	}
	end, err := parse(job.End[""])
	if err != nil {
		return source.FetchOptions{}, err
	}
	opts := source.FetchOptions{ProjectID: job.Project, StartDate: start, EndDate: end}
	for series, e := range job.End {
		if series == "" {
			continue
		}
		ws, err := parse(job.Start[series])
		if err != nil {
			return opts, err
		}
		we, err := parse(e)
		if err != nil {
			return opts, err
		}
		if opts.Windows == nil {
			opts.Windows = map[string]source.Window{}
		}
		opts.Windows[series] = source.Window{Start: ws, End: we}
	}
	return opts, nil
}

func replayJob(ctx context.Context, client *http.Client, cfg *config.Config, desc fetchSourceDescriptor, job archive.Job) ([]source.Record, map[string][]source.ContentEntry, error) {
	opts, err := jobOptions(job)
	if err != nil {
		return nil, nil, fmt.Errorf("manifest options: %w", err)
	}
	ctx = source.WithRequestInfo(ctx, source.RequestInfo{Source: job.Source, Project: job.Project, Target: job.Target})
	content := map[string][]source.ContentEntry{}

	if desc.eventFactory != nil {
		snap, ok := desc.eventFactory(client, replayTokens, cfg, job.Target).(snapshotFetcher)
		if !ok {
			return nil, nil, fmt.Errorf("%s archives no requests of its own", job.Source)
		}
		if err := snap.FetchSnapshots(ctx, opts, job.SnapshotRefs); err != nil {
			return nil, nil, err
		}
		for filename, entries := range snap.ContentByFilename() {
			content[filename] = append(content[filename], entries...)
		}
		return snap.Records(), content, nil
	}

	src := desc.metricFactory(client, replayTokens, cfg, job.Target)
	records, err := src.Fetch(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	if cp, ok := src.(source.ContentProvider); ok {
		if entries := cp.ContentEntries(); len(entries) > 0 {
			content[cp.ContentFilename()] = entries
		}
	}
	return records, content, nil
}
