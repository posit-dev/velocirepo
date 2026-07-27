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

// contentKey identifies a content entry for upsert. It includes Target (the
// feed URL / channel) alongside ID so that two distinct feeds sharing one
// content file — e.g. different RSS feeds whose URLs slug to the same
// filename — never overwrite each other's entries when their item IDs happen
// to collide.
type contentKey struct {
	target string
	id     string
}

func mergeContentEntries(existing, incoming []source.ContentEntry) []source.ContentEntry {
	byKey := make(map[contentKey]source.ContentEntry, len(existing))
	var order []contentKey

	key := func(e source.ContentEntry) contentKey {
		return contentKey{target: e.Target, id: e.ID}
	}

	for _, e := range existing {
		k := key(e)
		if _, exists := byKey[k]; !exists {
			order = append(order, k)
		}
		byKey[k] = e
	}

	for _, e := range incoming {
		k := key(e)
		if _, exists := byKey[k]; !exists {
			order = append(order, k)
		}
		byKey[k] = e
	}

	result := make([]source.ContentEntry, 0, len(order))
	for _, k := range order {
		result = append(result, byKey[k])
	}
	return result
}
