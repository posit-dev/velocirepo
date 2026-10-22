package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/posit-dev/velocirepo/internal/source"
)

// ExportTables are the tables an export writes, in this order.
var ExportTables = []string{"projects", "targets", "metrics", "metrics_filled", "indicators", "events", "content", "meta"}

// ExportDBName is the file name of an export in the duckdb format.
const ExportDBName = "velocirepo.duckdb"

type ExportOptions struct {
	DataDir string
	OutDir  string
	// Format is parquet, csv or duckdb.
	Format     string
	Source     string
	Project    string
	Projects   []ProjectInfo
	Indicators []IndicatorDef
	// BuiltAt is meta.built_at; the time of the export if zero.
	BuiltAt time.Time
}

// Exported is the data of an export, materialized as plain tables in the
// schema "export" of an in-memory database: the same tables and columns in
// every format.
type Exported struct {
	DB     *sql.DB
	Tables []string
	// Columns are the extra keys that became columns, per table.
	Columns map[string][]source.ColumnInfo
}

// Close closes the in-memory database.
func (x *Exported) Close() error {
	return x.DB.Close()
}

// Export materializes the data and writes it to opts.OutDir.
func Export(opts ExportOptions) ([]string, error) {
	x, err := Materialize(opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = x.Close() }()
	return x.Write(opts.OutDir, opts.Format)
}

// Materialize reads the data once and builds the export's tables, with the
// filters of opts. The tables are standalone: no views, no JSON-typed
// columns (a DuckDB without the json extension can read them), and the
// extra keys sources declare as columns are columns of their own.
func Materialize(opts ExportOptions) (*Exported, error) {
	switch opts.Format {
	case "", "parquet", "csv", "duckdb":
	default:
		return nil, fmt.Errorf("unsupported format %q (use parquet, csv or duckdb)", opts.Format)
	}

	db, err := openLiveDB(opts.DataDir, opts.Projects, opts.Indicators)
	if err != nil {
		return nil, err
	}
	x := &Exported{DB: db, Columns: map[string][]source.ColumnInfo{}}
	if err := x.build(opts); err != nil {
		_ = db.Close()
		return nil, err
	}
	return x, nil
}

func (x *Exported) build(opts ExportOptions) error {
	// Stage the views that read JSONL in the order they depend on each other,
	// repointing each at its staged table, so every file is parsed once:
	// metrics counts the staged events, and metrics_filled and indicators
	// read the staged metrics.
	for _, name := range []string{"events", "__velocirepo_metric_watermarks", "metrics", "content"} {
		if _, err := x.DB.Exec(fmt.Sprintf("CREATE TABLE __staged_%s AS SELECT * FROM %s", strings.TrimPrefix(name, "__"), name)); err != nil {
			return fmt.Errorf("stage %s: %w", name, err)
		}
		if _, err := x.DB.Exec(fmt.Sprintf("CREATE OR REPLACE VIEW %s AS SELECT * FROM __staged_%s", name, strings.TrimPrefix(name, "__"))); err != nil {
			return fmt.Errorf("repoint %s: %w", name, err)
		}
	}
	if _, err := x.DB.Exec("CREATE SCHEMA export"); err != nil {
		return fmt.Errorf("create export schema: %w", err)
	}

	for _, table := range ExportTables {
		if !exportsTable(table, opts.Source) {
			continue
		}
		query, err := x.exportQuery(table, opts)
		if err != nil {
			return err
		}
		if _, err := x.DB.Exec(fmt.Sprintf("CREATE TABLE export.%s AS %s", table, query)); err != nil {
			return fmt.Errorf("export %s: %w", table, err)
		}
		x.Tables = append(x.Tables, table)
	}

	var jsonColumns []string
	rows, err := x.DB.Query(`SELECT table_name || '.' || column_name FROM duckdb_columns()
		WHERE schema_name = 'export' AND data_type = 'JSON' ORDER BY ALL`)
	if err != nil {
		return fmt.Errorf("check column types: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return err
		}
		jsonColumns = append(jsonColumns, c)
	}
	if len(jsonColumns) > 0 {
		return fmt.Errorf("export has JSON-typed columns: %s", strings.Join(jsonColumns, ", "))
	}
	return rows.Err()
}

// exportsTable reports whether an export filtered to a source has a table.
// "projects" is the source name of the projects table alone.
func exportsTable(table, source string) bool {
	if source == "projects" {
		return table == "projects"
	}
	return true
}

func (x *Exported) exportQuery(table string, opts ExportOptions) (string, error) {
	var conditions []string
	if opts.Project != "" {
		col := "project"
		if table == "projects" {
			col = "id"
		}
		if table != "meta" {
			conditions = append(conditions, col+" = "+SQLStringLiteral(opts.Project))
		}
	}
	if opts.Source != "" && opts.Source != "projects" {
		switch table {
		case "projects":
			conditions = append(conditions, "id IN (SELECT project FROM targets WHERE source = "+SQLStringLiteral(opts.Source)+")")
		case "meta":
		default:
			conditions = append(conditions, "source = "+SQLStringLiteral(opts.Source))
		}
	}
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	switch table {
	case "projects":
		// Fields a project doesn't set are NULL, not empty strings.
		return `SELECT id, NULLIF(name, '') AS name, NULLIF(description, '') AS description, NULLIF(color, '') AS color,
			tags, NULLIF(website, '') AS website, NULLIF(logo, '') AS logo
			FROM projects` + where + " ORDER BY id", nil
	case "targets":
		return "SELECT * FROM targets" + where + " ORDER BY project, source, target", nil
	case "metrics", "metrics_filled", "indicators":
		cols, err := x.extraColumns(table, "metrics", "SELECT extra FROM "+table+where)
		if err != nil {
			return "", err
		}
		order := " ORDER BY project, source, target, metric, date, extra"
		if table == "indicators" {
			order = " ORDER BY project, source, target, metric, indicator, date, extra"
		}
		return "SELECT * EXCLUDE (extra)" + extraSelect(cols) + ", CAST(extra AS VARCHAR) AS extra FROM " + table + where + order, nil
	case "events":
		return `SELECT project, source, type, target, datetime, ref, "user", CAST(extra AS VARCHAR) AS extra
			FROM events` + where + ` ORDER BY project, source, target, datetime, type, ref, "user"`, nil
	case "content":
		cols, err := x.extraColumns(table, "content", "SELECT extra FROM content"+where)
		if err != nil {
			return "", err
		}
		return `SELECT project, source, target, id, ref, title, description, content, published_at, updated_at, url, duration,
			CAST(tags AS VARCHAR[]) AS tags, type` + extraSelect(cols) + `, CAST(extra AS VARCHAR) AS extra
			FROM content` + where + " ORDER BY project, source, target, id", nil
	case "meta":
		// One row: the latest day of the exported metrics and when the
		// export was made.
		builtAt := opts.BuiltAt
		if builtAt.IsZero() {
			builtAt = time.Now()
		}
		return fmt.Sprintf("SELECT (SELECT MAX(date) FROM export.metrics) AS as_of, TIMESTAMP '%s' AS built_at",
			builtAt.UTC().Format("2006-01-02 15:04:05")), nil
	}
	return "", fmt.Errorf("unknown export table %q", table)
}

// extraColumns returns the declared extra keys of a table family ("metrics"
// or "content") that the exported rows have, in the order sources declare
// them, and records them as the table's columns.
func (x *Exported) extraColumns(table, family, rowsQuery string) ([]source.ColumnInfo, error) {
	present := map[string]bool{}
	rows, err := x.DB.Query(`SELECT DISTINCT unnest(json_keys(extra)) FROM (` + rowsQuery + `) WHERE json_type(extra) = 'OBJECT'`)
	if err != nil {
		return nil, fmt.Errorf("read the extra keys of %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		present[key] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var cols []source.ColumnInfo
	seen := map[string]bool{}
	for _, m := range source.Metas() {
		for _, c := range m.Columns {
			if c.Table == family && present[c.Key] && !seen[c.Key] {
				seen[c.Key] = true
				cols = append(cols, c)
			}
		}
	}
	x.Columns[table] = cols
	return cols, nil
}

func extraSelect(cols []source.ColumnInfo) string {
	var b strings.Builder
	for _, c := range cols {
		value := "extra->>" + SQLStringLiteral(c.Key)
		if c.Type == "integer" {
			value = "TRY_CAST(" + value + " AS INTEGER)"
		}
		fmt.Fprintf(&b, ", %s AS %q", value, c.Key)
	}
	return b.String()
}

// Write writes the export's tables to outDir: one file per table for
// parquet and csv, one database for duckdb. It returns the files written.
func (x *Exported) Write(outDir, format string) ([]string, error) {
	if format == "" {
		format = "parquet"
	}
	absOut, err := filepath.Abs(outDir)
	if err != nil {
		return nil, fmt.Errorf("resolve output dir: %w", err)
	}
	if err := os.MkdirAll(absOut, 0755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	if format == "duckdb" {
		path, err := x.writeDuckDB(absOut)
		if err != nil {
			return nil, err
		}
		return []string{path}, nil
	}

	var copyFmt string
	switch format {
	case "parquet":
		copyFmt = "PARQUET"
	case "csv":
		copyFmt = "CSV, HEADER"
	default:
		return nil, fmt.Errorf("unsupported format %q (use parquet, csv or duckdb)", format)
	}
	var written []string
	for _, table := range x.Tables {
		outFile := filepath.Join(absOut, table+"."+format)
		query := fmt.Sprintf(`COPY export.%s TO '%s' (FORMAT %s)`, table, escapeSQLString(outFile), copyFmt)
		if _, err := x.DB.Exec(query); err != nil {
			return nil, fmt.Errorf("export %s: %w", table, err)
		}
		written = append(written, outFile)
	}
	return written, nil
}

// writeDuckDB writes the tables to a new database file, which replaces the
// previous one only once it's complete.
func (x *Exported) writeDuckDB(absOut string) (string, error) {
	path := filepath.Join(absOut, ExportDBName)
	tmp, err := os.CreateTemp(absOut, ".velocirepo-export-*.duckdb")
	if err != nil {
		return "", fmt.Errorf("create temp db file: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	// DuckDB creates the file itself.
	_ = os.Remove(tmpPath)
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpPath)
			_ = os.Remove(tmpPath + ".wal")
		}
	}()

	if _, err := x.DB.Exec("ATTACH '" + escapeSQLString(tmpPath) + "' AS __export_file"); err != nil {
		return "", fmt.Errorf("create %s: %w", path, err)
	}
	for _, table := range x.Tables {
		if _, err := x.DB.Exec(fmt.Sprintf("CREATE TABLE __export_file.%s AS SELECT * FROM export.%s", table, table)); err != nil {
			_, _ = x.DB.Exec("DETACH __export_file")
			return "", fmt.Errorf("export %s: %w", table, err)
		}
	}
	if _, err := x.DB.Exec("DETACH __export_file"); err != nil {
		return "", fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", fmt.Errorf("replace %s: %w", path, err)
	}
	ok = true
	return path, nil
}
