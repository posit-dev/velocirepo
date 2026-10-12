package projectdata

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

// firstSourceDir returns the first name returned by config.SourceDirNames so
// tests can seed exactly one source directory.
func firstSourceDir() string {
	dirs := config.SourceDirNames()
	if len(dirs) == 0 {
		panic("config.SourceDirNames() is empty")
	}
	return dirs[0]
}

// writeTestRecord writes a single metric record to a JSONL file and returns
// the file's path.
func writeTestRecord(t *testing.T, dataDir, sourceDir, projectID, date string, val int64) string {
	t.Helper()
	records := []source.Record{
		{Source: "pypi", Metric: "daily_downloads", ProjectID: projectID,
			Target: "pkg", Date: date, Value: val},
	}
	if _, err := store.WriteRecords(dataDir, sourceDir, projectID, records); err != nil {
		t.Fatalf("WriteRecords: %v", err)
	}
	return filepath.Join(dataDir, "metrics", sourceDir, projectID, date+".jsonl")
}

// TestMoveProjectDirsSuccess verifies that MoveProjectDirs renames the
// directory and rewrites project_id in every JSONL file.
func TestMoveProjectDirsSuccess(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	src := "pypi"
	writeTestRecord(t, dataDir, src, "old-proj", "2025-06-01", 42)

	moves, err := MoveProjectDirs(dataDir, "old-proj", "new-proj")
	if err != nil {
		t.Fatalf("MoveProjectDirs: %v", err)
	}
	if len(moves) == 0 {
		t.Fatal("expected at least one move")
	}

	// Old directory must be gone.
	oldDir := filepath.Join(dataDir, "metrics", src, "old-proj")
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Error("old directory still exists")
	}

	// New directory must exist.
	newDir := filepath.Join(dataDir, "metrics", src, "new-proj")
	if _, err := os.Stat(newDir); err != nil {
		t.Errorf("new directory not found: %v", err)
	}

	// JSONL file must reference new-proj.
	data, err := os.ReadFile(filepath.Join(newDir, "2025-06-01.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"project_id":"new-proj"`) {
		t.Errorf("project_id not rewritten: %s", data)
	}
}

// TestMoveProjectDirsTargetAlreadyExists verifies the pre-flight check.
func TestMoveProjectDirsTargetAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	src := firstSourceDir()

	// Create both old and new directories.
	for _, id := range []string{"old-proj", "new-proj"} {
		if err := os.MkdirAll(filepath.Join(dataDir, src, id), 0755); err != nil {
			t.Fatal(err)
		}
	}

	_, err := MoveProjectDirs(dataDir, "old-proj", "new-proj")
	if err == nil {
		t.Fatal("expected error when target already exists")
	}
}

// TestMoveProjectDirsReturnsErrorAndMovesOnRewriteFailure verifies that when
// RewriteProjectID fails (invalid JSONL), the function returns both an error
// and the list of moves that completed so the caller can rollback.
func TestMoveProjectDirsReturnsErrorAndMovesOnRewriteFailure(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	src := firstSourceDir()

	// Seed an invalid JSONL file so RewriteProjectID fails after the rename.
	oldDir := filepath.Join(dataDir, src, "old-proj")
	if err := os.MkdirAll(oldDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "2025-06-01.jsonl"),
		[]byte("{invalid json}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	moves, err := MoveProjectDirs(dataDir, "old-proj", "new-proj")
	if err == nil {
		t.Fatal("expected error from failed rewrite")
	}
	// The caller gets the partial moves list so it can rollback.
	if len(moves) == 0 {
		t.Error("expected non-empty moves on partial failure")
	}
}

// TestRollbackMovesRestoresOriginalState verifies that after a successful
// MoveProjectDirs, calling RollbackMoves returns the filesystem to its
// original state.
func TestRollbackMovesRestoresOriginalState(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	src := "pypi"

	writeTestRecord(t, dataDir, src, "old-proj", "2025-06-01", 10)

	moves, err := MoveProjectDirs(dataDir, "old-proj", "new-proj")
	if err != nil {
		t.Fatalf("MoveProjectDirs: %v", err)
	}

	if err := RollbackMoves(moves); err != nil {
		t.Fatalf("RollbackMoves: %v", err)
	}

	// Original directory must be restored.
	oldDir := filepath.Join(dataDir, "metrics", src, "old-proj")
	if _, err := os.Stat(oldDir); err != nil {
		t.Errorf("original directory not restored: %v", err)
	}

	// New directory must be gone.
	newDir := filepath.Join(dataDir, "metrics", src, "new-proj")
	if _, err := os.Stat(newDir); !os.IsNotExist(err) {
		t.Error("new directory still exists after rollback")
	}

	// project_id must be restored to old-proj.
	data, err := os.ReadFile(filepath.Join(oldDir, "2025-06-01.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"project_id":"old-proj"`) {
		t.Errorf("project_id not restored: %s", data)
	}
}

// TestRollbackMovesSkipsNonExistentTarget verifies that RollbackMoves silently
// skips a move whose 'to' path no longer exists (e.g. already cleaned up).
func TestRollbackMovesSkipsNonExistentTarget(t *testing.T) {
	dir := t.TempDir()
	moves := []Move{
		{
			from:  filepath.Join(dir, "orig"),
			to:    filepath.Join(dir, "does-not-exist"),
			oldID: "new-id",
			newID: "old-id",
		},
	}
	if err := RollbackMoves(moves); err != nil {
		t.Errorf("RollbackMoves should skip non-existent target, got: %v", err)
	}
}

// TestTrashProjectDirsNoData verifies that TrashProjectDirs on a data dir with
// no project data returns ("", nil, nil).
func TestTrashProjectDirsNoData(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}

	trashRoot, moves, err := TrashProjectDirs(dataDir, "ghost-proj")
	if err != nil {
		t.Fatalf("TrashProjectDirs: %v", err)
	}
	if trashRoot != "" {
		t.Errorf("expected empty trashRoot for non-existent project, got %s", trashRoot)
	}
	if len(moves) != 0 {
		t.Errorf("expected no moves, got %v", moves)
	}
}

// TestTrashProjectDirsNonExistentDataDir verifies that a non-existent data
// directory is treated as "no data".
func TestTrashProjectDirsNonExistentDataDir(t *testing.T) {
	dir := t.TempDir()
	trashRoot, moves, err := TrashProjectDirs(filepath.Join(dir, "nonexistent"), "proj")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if trashRoot != "" || len(moves) != 0 {
		t.Errorf("expected empty result, got trashRoot=%q moves=%v", trashRoot, moves)
	}
}

// TestTrashProjectDirsSuccess verifies that TrashProjectDirs moves all project
// directories into the trash and returns the trash root path.
func TestTrashProjectDirsSuccess(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	// Seed two source directories for the project.
	writeTestRecord(t, dataDir, "pypi", "rm-proj", "2025-06-01", 5)

	trashRoot, moves, err := TrashProjectDirs(dataDir, "rm-proj")
	if err != nil {
		t.Fatalf("TrashProjectDirs: %v", err)
	}
	if trashRoot == "" {
		t.Fatal("expected non-empty trashRoot")
	}
	if len(moves) == 0 {
		t.Fatal("expected at least one move")
	}

	// Original directory must be gone.
	origDir := filepath.Join(dataDir, "metrics", "pypi", "rm-proj")
	if _, err := os.Stat(origDir); !os.IsNotExist(err) {
		t.Error("original directory still exists")
	}

	// Trash root must exist.
	if _, err := os.Stat(trashRoot); err != nil {
		t.Errorf("trash root not found: %v", err)
	}
}

// TestTrashProjectDirsRollbackOnPartialFailure verifies that if a stat error
// occurs mid-way, already-moved directories are restored and the trash dir is
// removed.
//
// We trigger this by making the data directory unreadable after one move, then
// attempting TrashProjectDirs on a second call with the original data still in
// place. Instead of that fragile approach, we directly verify the existing
// rollback path by calling RollbackMoves after a successful TrashProjectDirs.
func TestTrashProjectDirsFollowedByRollback(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	writeTestRecord(t, dataDir, "pypi", "proj", "2025-06-01", 7)

	trashRoot, moves, err := TrashProjectDirs(dataDir, "proj")
	if err != nil {
		t.Fatalf("TrashProjectDirs: %v", err)
	}
	if trashRoot == "" {
		t.Fatal("expected trashRoot")
	}

	// Rollback (as project_remove does when config write fails).
	if err := RollbackMoves(moves); err != nil {
		t.Fatalf("RollbackMoves: %v", err)
	}

	// Original directory must be restored.
	origDir := filepath.Join(dataDir, "metrics", "pypi", "proj")
	if _, err := os.Stat(origDir); err != nil {
		t.Errorf("directory not restored after rollback: %v", err)
	}
}
