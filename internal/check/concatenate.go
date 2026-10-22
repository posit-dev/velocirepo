package check

import (
	"context"
	"fmt"
)

// RecordsMoved checks a concatenation: it only moves records and events
// between files, so the set of records before and after it is identical.
// Any difference is a concatenate.records-moved error.
func RecordsMoved(ctx context.Context, dataDir string, d Diff) ([]Finding, error) {
	diffs, _, err := CompareData(ctx, dataDir, Diff{Base: d.Base, Head: d.Head})
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, pd := range diffs {
		if pd.Category == "content" {
			continue
		}
		msg := fmt.Sprintf("concatenation changed %s: %s removed, %s added, %s changed",
			noun(pd.Category), thousands(pd.Removed()), thousands(pd.Added()), thousands(pd.Changed()))
		out = append(out, NewFinding("concatenate.records-moved", Error, pd.Source, pd.Project, "", pd.Category, msg).WithPaths(pd.Dir+"/"))
	}
	return out, nil
}
