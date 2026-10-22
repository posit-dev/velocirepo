package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --data-dict describes the duckdb format: it implies it, and refuses the
// others.
func TestExportDataDictIsForDuckDB(t *testing.T) {
	cfgPath, _ := setupBadgeConfig(t)
	out := filepath.Join(t.TempDir(), "out")

	if _, buf, err := execCmd(cfgPath, "export", out, "--data-dict"); err != nil {
		t.Fatalf("export --data-dict: %v\n%s", err, buf.String())
	}
	for _, name := range []string{"velocirepo.duckdb", "data-dict.yaml"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("export --data-dict wrote no %s", name)
		}
	}

	_, _, err := execCmd(cfgPath, "export", filepath.Join(t.TempDir(), "out"), "--data-dict", "--format", "parquet")
	if err == nil || !strings.Contains(err.Error(), "duckdb") {
		t.Errorf("export --data-dict --format parquet: err = %v", err)
	}
}
