package gate

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/posit-dev/velocirepo/internal/check"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

// nonBlocking are the checks whose errors never hold data back: the diff
// checks undo what they find instead, a failed job stores nothing, and the
// others are about stored data or the run itself.
var nonBlocking = []string{"fetch.", "diff.", "data.freshness", "data.volume", "reconcile.", "quota.", "archive.", "gate.", "concatenate."}

func blocks(f check.Finding) bool {
	if f.Accepted || f.Severity != check.Error {
		return false
	}
	for _, prefix := range nonBlocking {
		if strings.HasPrefix(f.Check, prefix) {
			return false
		}
	}
	return true
}

// blocked returns the sources and projects (source/project) whose new
// records are held back, and whether an error that doesn't belong to a
// project holds back everything. Only errors about the night's new records
// block; warnings never do.
func blocked(findings []check.Finding) (map[string]bool, bool) {
	out := map[string]bool{}
	global := false
	for _, f := range findings {
		if !blocks(f) {
			continue
		}
		if f.Source == "" || f.Project == "" {
			global = true
			continue
		}
		out[f.Source+"/"+f.Project] = true
	}
	return out, global
}

// applyRule undoes what a night isn't allowed to do: removed records are
// restored from HEAD, and changed values get their stored value back. Changes
// within a repair are left as they are.
func (r *run) applyRule(diffs []check.ProjectDiff, repairs []check.Repair) error {
	inRepair := func(pd check.ProjectDiff, l check.Line) bool {
		for _, rep := range repairs {
			date := l.Date
			if pd.Category == store.ContentDataDir {
				date = ""
			}
			if rep.Covers(pd.Source, pd.Project, date) {
				return true
			}
		}
		return false
	}
	for _, pd := range diffs {
		dir := filepath.Join(r.dataDir, filepath.FromSlash(pd.Dir))
		byFile := map[string][]check.Line{}
		changed := map[string]bool{}
		for _, l := range pd.RemovedLines() {
			if !inRepair(pd, l) {
				byFile[l.File] = append(byFile[l.File], l)
			}
		}
		for _, l := range pd.ChangedLines() {
			if !inRepair(pd, l) {
				byFile[l.File] = append(byFile[l.File], l)
				var rec source.Record
				if json.Unmarshal(l.Data, &rec) == nil {
					changed[check.RecordKey(rec)] = true
				}
			}
		}
		for file, restore := range byFile {
			path := filepath.Join(dir, file)
			if err := restoreFile(pd, path, restore, changed, r.dataDir); err != nil {
				return err
			}
		}
	}
	return nil
}

func restoreFile(pd check.ProjectDiff, path string, restore []check.Line, changed map[string]bool, dataDir string) error {
	switch pd.Category {
	case store.MetricsDir:
		stored, err := readOrEmpty(store.ReadRecords, path)
		if err != nil {
			return err
		}
		kept := stored[:0]
		for _, rec := range stored {
			if !changed[check.RecordKey(rec)] {
				kept = append(kept, rec)
			}
		}
		for _, l := range restore {
			var rec source.Record
			if err := json.Unmarshal(l.Data, &rec); err != nil {
				return err
			}
			kept = append(kept, rec)
		}
		return store.WriteRecordsFile(path, kept)
	case store.EventsDir:
		stored, err := readOrEmpty(store.ReadEvents, path)
		if err != nil {
			return err
		}
		for _, l := range restore {
			var e source.Event
			if err := json.Unmarshal(l.Data, &e); err != nil {
				return err
			}
			stored = append(stored, e)
		}
		return store.WriteEventsFile(path, stored)
	case store.ContentDataDir:
		var entries []source.ContentEntry
		for _, l := range restore {
			var c source.ContentEntry
			if err := json.Unmarshal(l.Data, &c); err != nil {
				return err
			}
			entries = append(entries, c)
		}
		return store.WriteContent(dataDir, pd.Source, pd.Project, filepath.Base(path), entries)
	}
	return nil
}

func readOrEmpty[T any](read func(string) ([]T, error), path string) ([]T, error) {
	items, err := read(path)
	if err != nil && !isNotExist(err) {
		return nil, err
	}
	return items, nil
}
