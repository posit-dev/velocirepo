package store

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/posit-dev/velocirepo/internal/source"
)

func ContentDir(dataDir, sourceName, projectID string) string {
	return ContentProjectDir(dataDir, sourceName, projectID)
}

func WriteContent(dataDir, sourceName, projectID, filename string, entries []source.ContentEntry) error {
	dir := ContentDir(dataDir, sourceName, projectID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	path := filepath.Join(dir, filename)

	existing, err := ReadContent(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read existing content: %w", err)
	}

	merged := mergeContentEntries(existing, entries)

	// Stamp project_id on every line from the write path, so all content
	// sources record it uniformly and pre-existing lines are backfilled on the
	// next write. Keeps content JSONL consistent with metrics/events and lets
	// RewriteProjectID keep them in sync on rename.
	for i := range merged {
		merged[i].ProjectID = projectID
	}

	if err := writeJSONLAtomic(path, merged, "content entry"); err != nil {
		return err
	}

	ensureSchemaVersion(dataDir)
	return nil
}

func ReadContent(path string) ([]source.ContentEntry, error) {
	return readJSONL[source.ContentEntry](path, readJSONLOptions{skipInvalid: true})
}

func mergeContentEntries(existing, incoming []source.ContentEntry) []source.ContentEntry {
	byID := make(map[string]source.ContentEntry, len(existing))
	var order []string

	for _, e := range existing {
		byID[e.ID] = e
		order = append(order, e.ID)
	}

	for _, e := range incoming {
		if _, exists := byID[e.ID]; !exists {
			order = append(order, e.ID)
		}
		byID[e.ID] = e
	}

	result := make([]source.ContentEntry, 0, len(order))
	for _, id := range order {
		result = append(result, byID[id])
	}
	return result
}
