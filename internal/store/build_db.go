package store

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/marcboeker/go-duckdb"
)

func BuildDB(dataDir string, projects []ProjectInfo, indicators []IndicatorDef) error {
	absDir, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("resolve data dir: %w", err)
	}

	dbPath := filepath.Join(absDir, "velocirepo.duckdb")

	_ = os.Remove(dbPath)
	_ = os.Remove(dbPath + ".wal")

	db, err := sql.Open("duckdb", dbPath)
	if err != nil {
		return fmt.Errorf("open duckdb file: %w", err)
	}
	defer func() { _ = db.Close() }()

	if err := createEventsViewRelative(db, absDir); err != nil {
		return err
	}
	if err := createMetricsViewRelative(db, absDir); err != nil {
		return err
	}
	if err := createMetricWatermarksView(db, absDir); err != nil {
		return err
	}
	if err := createMetricsFilledView(db); err != nil {
		return err
	}
	if err := createContentViewRelative(db, absDir); err != nil {
		return err
	}
	if err := createProjectsTable(db, projects); err != nil {
		return err
	}
	if err := createIndicatorsView(db, indicators); err != nil {
		return err
	}

	return nil
}

func createEventsViewRelative(db *sql.DB, absDir string) error {
	glob := filepath.ToSlash(filepath.Join(absDir, "events", "*", "*", "*.jsonl"))

	if !globHasMatches(glob) {
		slog.Debug("no event files found, creating empty view")
		return createEmptyEventsView(db)
	}

	query := fmt.Sprintf(`CREATE OR REPLACE VIEW events AS
		SELECT
			project_id AS project,
			source,
			type,
			target,
			CAST(datetime AS TIMESTAMP) AS datetime,
			tags
		FROM read_json('%s',
			format='newline_delimited',
			columns={source: 'VARCHAR', type: 'VARCHAR', project_id: 'VARCHAR', target: 'VARCHAR', datetime: 'VARCHAR', tags: 'JSON'})`,
		escapeSQLString(glob))

	if _, err := db.Exec(query); err != nil {
		slog.Debug("events view creation failed, using empty view", "error", err)
		return createEmptyEventsView(db)
	}
	return nil
}

func createMetricsViewRelative(db *sql.DB, absDir string) error {
	query := metricsViewSQL(absDir)
	if _, err := db.Exec(query); err != nil {
		slog.Debug("metrics view creation failed, using empty view", "error", err)
		return createEmptyMetricsView(db)
	}
	return nil
}

func globHasMatches(pattern string) bool {
	matches, err := filepath.Glob(pattern)
	return err == nil && len(matches) > 0
}

func createContentViewRelative(db *sql.DB, absDir string) error {
	glob := filepath.ToSlash(filepath.Join(absDir, ContentDataDir, "*", "*", "*.jsonl"))

	if !globHasMatches(glob) {
		slog.Debug("no content files found, creating empty view")
		return createEmptyContentView(db)
	}

	query := fmt.Sprintf(`CREATE OR REPLACE VIEW content AS
		SELECT
			project_id AS project,
			source,
			target,
			id,
			title,
			description,
			content,
			TRY_CAST(published_at AS TIMESTAMP) AS published_at,
			TRY_CAST(updated_at AS TIMESTAMP) AS updated_at,
			url,
			duration,
			tags,
			type,
			extra
		FROM read_json('%s',
			format='newline_delimited',
			columns={project_id: 'VARCHAR', source: 'VARCHAR', target: 'VARCHAR', id: 'VARCHAR', title: 'VARCHAR', description: 'VARCHAR', content: 'VARCHAR', published_at: 'VARCHAR', updated_at: 'VARCHAR', url: 'VARCHAR', duration: 'BIGINT', tags: 'JSON', type: 'VARCHAR', extra: 'JSON'})`,
		escapeSQLString(glob))

	if _, err := db.Exec(query); err != nil {
		slog.Debug("content view creation failed, using empty view", "error", err)
		return createEmptyContentView(db)
	}
	return nil
}

func createProjectsTable(db *sql.DB, projects []ProjectInfo) error {
	_, err := db.Exec(`DROP TABLE IF EXISTS projects`)
	if err != nil {
		return fmt.Errorf("drop projects table: %w", err)
	}

	_, err = db.Exec(`CREATE TABLE projects (
		id VARCHAR,
		name VARCHAR,
		description VARCHAR,
		color VARCHAR,
		tags VARCHAR[],
		website VARCHAR,
		logo VARCHAR
	)`)
	if err != nil {
		return fmt.Errorf("create projects table: %w", err)
	}

	if len(projects) == 0 {
		return nil
	}

	var rows []string
	for _, p := range projects {
		tags := "NULL::VARCHAR[]"
		if len(p.Tags) > 0 {
			var escaped []string
			for _, t := range p.Tags {
				escaped = append(escaped, "'"+escapeSQLString(t)+"'")
			}
			tags = "[" + strings.Join(escaped, ", ") + "]"
		}
		row := fmt.Sprintf("('%s', '%s', '%s', '%s', %s, '%s', '%s')",
			escapeSQLString(p.ID),
			escapeSQLString(p.Name),
			escapeSQLString(p.Description),
			escapeSQLString(p.Color),
			tags,
			escapeSQLString(p.Website),
			escapeSQLString(p.Logo),
		)
		rows = append(rows, row)
	}

	query := `INSERT INTO projects VALUES ` + strings.Join(rows, ", ")
	if _, err := db.Exec(query); err != nil {
		return fmt.Errorf("insert projects: %w", err)
	}

	return nil
}
