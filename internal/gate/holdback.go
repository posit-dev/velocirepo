package gate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/posit-dev/velocirepo/internal/check"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

// HeldBackDir holds the data the gate holds back, next to the data
// directory: queries, views and build-db only read the data directory, so
// held-back records are invisible to them.
const HeldBackDir = "held-back"

func isNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }

// heldBack is everything a night changed for one source and project: its
// new records and events, its content, and its watermarks.
type heldBack struct {
	Records    []source.Record
	Events     []source.Event
	Content    map[string][]source.ContentEntry
	Watermarks map[string]map[string]string // series → target → date
}

func (r *run) heldBackRoot() string {
	return filepath.Join(r.root, filepath.FromSlash(r.veloRel), HeldBackDir)
}

func (r *run) heldBackPath(key string) string {
	return filepath.Join(r.heldBackRoot(), filepath.FromSlash(key))
}

// heldBackProjects returns the held-back sources and projects, as
// source/project.
func (r *run) heldBackProjects() ([]string, error) {
	sources, err := os.ReadDir(r.heldBackRoot())
	if isNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, s := range sources {
		if !s.IsDir() {
			continue
		}
		projects, err := os.ReadDir(filepath.Join(r.heldBackRoot(), s.Name()))
		if err != nil {
			return nil, err
		}
		for _, p := range projects {
			if p.IsDir() {
				out = append(out, s.Name()+"/"+p.Name())
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func splitKey(key string) (string, string) {
	src, project, _ := strings.Cut(key, "/")
	return src, project
}

// readHeldBack reads a held-back project.
func (r *run) readHeldBack(key string) (heldBack, error) {
	dir := r.heldBackPath(key)
	h := heldBack{Content: map[string][]source.ContentEntry{}, Watermarks: map[string]map[string]string{}}
	var err error
	if h.Records, err = readOrEmpty(store.ReadRecords, filepath.Join(dir, "records.jsonl")); err != nil {
		return h, err
	}
	if h.Events, err = readOrEmpty(store.ReadEvents, filepath.Join(dir, "events.jsonl")); err != nil {
		return h, err
	}
	files, _ := filepath.Glob(filepath.Join(dir, "content", "*.jsonl"))
	for _, f := range files {
		entries, err := store.ReadContent(f)
		if err != nil {
			return h, err
		}
		h.Content[filepath.Base(f)] = entries
	}
	data, err := os.ReadFile(filepath.Join(dir, "watermarks.json"))
	if err != nil && !isNotExist(err) {
		return h, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &h.Watermarks); err != nil {
			return h, fmt.Errorf("held-back watermarks of %s: %w", key, err)
		}
	}
	return h, nil
}

func (r *run) writeHeldBack(key string, h heldBack) error {
	dir := r.heldBackPath(key)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := store.WriteRecordsFile(filepath.Join(dir, "records.jsonl"), h.Records); err != nil {
		return err
	}
	if err := store.WriteEventsFile(filepath.Join(dir, "events.jsonl"), h.Events); err != nil {
		return err
	}
	for name, entries := range h.Content {
		if err := os.MkdirAll(filepath.Join(dir, "content"), 0o755); err != nil {
			return err
		}
		data := []byte{}
		for _, e := range entries {
			line, err := json.Marshal(e)
			if err != nil {
				return err
			}
			data = append(append(data, line...), '\n')
		}
		if err := os.WriteFile(filepath.Join(dir, "content", name), data, 0o644); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(h.Watermarks, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "watermarks.json"), append(data, '\n'), 0o644)
}

// overlay writes a held-back project's data into the data directory through
// the store, so it goes into period files that were concatenated in the
// meantime, and the fetch continues from it: the watermarks, resume points
// and the filter for unchanged totals see "stored plus held back".
func (r *run) overlay(key string) error {
	h, err := r.readHeldBack(key)
	if err != nil {
		return err
	}
	src, project := splitKey(key)
	if len(h.Records) > 0 {
		if _, err := store.WriteRecords(r.dataDir, src, project, h.Records); err != nil {
			return err
		}
	}
	if len(h.Events) > 0 {
		if _, err := store.AddEvents(r.dataDir, src, project, h.Events); err != nil {
			return err
		}
	}
	for name, entries := range h.Content {
		if err := store.WriteContent(r.dataDir, src, project, name, entries); err != nil {
			return err
		}
	}
	for series, marks := range h.Watermarks {
		for target, date := range marks {
			if err := store.MarkFetchedSeries(r.dataDir, src, project, series, date, []string{target}); err != nil {
				return err
			}
		}
	}
	return nil
}

// holdBack moves everything the night changed for a source and project into
// its held-back directory, and leaves its paths in the data directory as
// they are on HEAD, so the stored watermarks never move past data that isn't
// stored.
func (r *run) holdBack(ctx context.Context, key string, diffs []check.ProjectDiff) error {
	src, project := splitKey(key)
	h := heldBack{Content: map[string][]source.ContentEntry{}, Watermarks: map[string]map[string]string{}}
	for _, pd := range diffs {
		if pd.Source != src || pd.Project != project {
			continue
		}
		for _, l := range pd.AddedLines() {
			switch pd.Category {
			case store.MetricsDir:
				var rec source.Record
				if err := json.Unmarshal(l.Data, &rec); err != nil {
					return err
				}
				h.Records = append(h.Records, rec)
			case store.EventsDir:
				var e source.Event
				if err := json.Unmarshal(l.Data, &e); err != nil {
					return err
				}
				h.Events = append(h.Events, e)
			}
		}
	}
	// Content is updated in place, so a changed file is kept whole.
	contentDir := store.ContentProjectDir(r.dataDir, src, project)
	changed, err := r.git.run(ctx, "status", "--porcelain", "--untracked-files=all", "--", contentDir)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(changed, "\n") {
		if len(line) < 4 || !strings.HasSuffix(line, ".jsonl") {
			continue
		}
		path := filepath.Join(r.root, filepath.FromSlash(strings.TrimSpace(line[3:])))
		entries, err := readOrEmpty(store.ReadContent, path)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			h.Content[filepath.Base(path)] = entries
		}
	}
	metricsDir := store.MetricsProjectDir(r.dataDir, src, project)
	names, _ := filepath.Glob(filepath.Join(metricsDir, "_watermark*.json"))
	for _, name := range names {
		series := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(name), "_watermark"), ".json")
		series = strings.TrimPrefix(series, "-")
		marks, err := store.SeriesWatermarks(r.dataDir, src, project, series)
		if err != nil {
			return err
		}
		if len(marks) > 0 {
			h.Watermarks[series] = marks
		}
	}
	if err := r.writeHeldBack(key, h); err != nil {
		return err
	}
	for _, dir := range []string{metricsDir, store.EventsProjectDir(r.dataDir, src, project), contentDir} {
		if err := r.resetToHead(ctx, dir); err != nil {
			return err
		}
	}
	return nil
}

// resetToHead puts a directory back as it is on HEAD.
func (r *run) resetToHead(ctx context.Context, dir string) error {
	rel, err := filepath.Rel(r.root, dir)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	if out, _ := r.git.run(ctx, "ls-tree", "--name-only", "HEAD", "--", rel); out != "" {
		if _, err := r.git.run(ctx, "checkout", "HEAD", "--", rel); err != nil {
			return err
		}
	}
	_, err = r.git.run(ctx, "clean", "-fdq", "--", rel)
	return err
}
