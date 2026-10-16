package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestQueryLiveRestrictedCannotReadFiles proves that QueryLiveRestricted
// enforces enable_external_access=false: read_text, read_csv, read_json, and
// a bare file-path string literal in FROM all fail, and the secret stored in
// the file is never returned in a result row.
func TestQueryLiveRestrictedCannotReadFiles(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}

	const secret = "s3cr3t-password"

	secretFile := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	csvFile := filepath.Join(dir, "secret.csv")
	if err := os.WriteFile(csvFile, []byte("value\n"+secret+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	jsonFile := filepath.Join(dir, "secret.json")
	if err := os.WriteFile(jsonFile, []byte(`[{"key":"`+secret+`"}]`), 0600); err != nil {
		t.Fatal(err)
	}

	mustFail := func(t *testing.T, name, query string) {
		t.Helper()
		rows, _, err := QueryLiveRestricted(dataDir, nil, nil, query)
		if err != nil {
			// Error is the expected outcome — external access is blocked.
			return
		}
		// If no error, make sure the secret value wasn't leaked in a result.
		for _, row := range rows {
			for _, v := range row {
				if s, ok := v.(string); ok && strings.Contains(s, secret) {
					t.Fatalf("%s: secret value was returned in a result row", name)
				}
			}
		}
		t.Fatalf("%s: expected an error from %q but got %d rows with no error", name, query, len(rows))
	}

	t.Run("read_text", func(t *testing.T) {
		mustFail(t, "read_text",
			fmt.Sprintf("SELECT read_text('%s')", escapeSQLString(secretFile)))
	})
	t.Run("read_csv", func(t *testing.T) {
		mustFail(t, "read_csv",
			fmt.Sprintf("SELECT * FROM read_csv('%s')", escapeSQLString(csvFile)))
	})
	t.Run("read_json", func(t *testing.T) {
		mustFail(t, "read_json",
			fmt.Sprintf("SELECT * FROM read_json('%s')", escapeSQLString(jsonFile)))
	})
	t.Run("from_string_literal", func(t *testing.T) {
		// DuckDB reads CSV files when FROM receives a string literal path.
		mustFail(t, "from_string_literal",
			fmt.Sprintf("SELECT * FROM '%s'", escapeSQLString(csvFile)))
	})
}

// TestQueryLiveRestrictedCannotReEnableExternalAccess proves that once
// enable_external_access is set to false by QueryLiveRestricted, a query
// cannot re-enable it within the same session.
func TestQueryLiveRestrictedCannotReEnableExternalAccess(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}

	_, _, err := QueryLiveRestricted(dataDir, nil, nil, "SET enable_external_access = true")
	if err == nil {
		t.Error("SET enable_external_access = true should return an error inside a restricted session")
	}
}
