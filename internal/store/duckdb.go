package store

import (
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	_ "github.com/marcboeker/go-duckdb"
)

type SchemaColumn struct {
	Table    string
	Column   string
	Type     string
	Nullable string
}

type ProjectInfo struct {
	ID          string
	Name        string
	Description string
	Color       string
	Tags        []string
	Website     string
	Logo        string
}

func QueryLive(dataDir string, projects []ProjectInfo, indicators []IndicatorDef, query string) ([]map[string]interface{}, []string, error) {
	db, err := openLiveDB(dataDir, projects, indicators)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = db.Close() }()

	return queryRows(db, query)
}

func QueryLiveRestricted(dataDir string, projects []ProjectInfo, indicators []IndicatorDef, query string) ([]map[string]interface{}, []string, error) {
	db, err := openLiveDB(dataDir, projects, indicators)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = db.Close() }()

	if err := materializeRestrictedTables(db); err != nil {
		return nil, nil, err
	}
	if _, err := db.Exec("SET enable_external_access = false"); err != nil {
		return nil, nil, fmt.Errorf("disable external access: %w", err)
	}

	return queryRows(db, query)
}

func QueryLiveParquet(dataDir string, projects []ProjectInfo, indicators []IndicatorDef, query string, outPath string) error {
	db, err := openLiveDB(dataDir, projects, indicators)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	copySQL := fmt.Sprintf(`COPY (%s) TO '%s' (FORMAT PARQUET)`, query, escapeSQLString(outPath))
	_, err = db.Exec(copySQL)
	return err
}

func SchemaLive(dataDir string, projects []ProjectInfo, indicators []IndicatorDef) ([]SchemaColumn, error) {
	db, err := openLiveDB(dataDir, projects, indicators)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	rows, err := db.Query("SELECT table_name, column_name, data_type, is_nullable FROM information_schema.columns WHERE table_name IN ('content', 'events', 'indicators', 'metrics', 'metrics_filled', 'projects') ORDER BY table_name, ordinal_position")
	if err != nil {
		return nil, fmt.Errorf("query schema: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var cols []SchemaColumn
	for rows.Next() {
		var c SchemaColumn
		if err := rows.Scan(&c.Table, &c.Column, &c.Type, &c.Nullable); err != nil {
			return nil, fmt.Errorf("scan schema row: %w", err)
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

func openLiveDB(dataDir string, projects []ProjectInfo, indicators []IndicatorDef) (*sql.DB, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, fmt.Errorf("open in-memory duckdb: %w", err)
	}
	db.SetMaxOpenConns(1)

	absDir, err := filepath.Abs(dataDir)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("resolve data dir: %w", err)
	}

	if err := createEventsView(db, absDir); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := createMetricsView(db, absDir); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := createMetricWatermarksView(db, absDir); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := createMetricsFilledView(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := createContentView(db, absDir); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := createProjectsView(db, projects); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := createIndicatorsView(db, indicators); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

func materializeRestrictedTables(db *sql.DB) error {
	tables := []string{"events", "metrics", "metrics_filled", "content", "projects", "indicators"}
	for _, table := range tables {
		query := fmt.Sprintf("CREATE TABLE __velocirepo_%s AS SELECT * FROM %s", table, table)
		if _, err := db.Exec(query); err != nil {
			return fmt.Errorf("materialize %s: %w", table, err)
		}
	}

	for _, view := range []string{"indicators", "projects", "content", "metrics_filled", "metrics", "events"} {
		if _, err := db.Exec("DROP VIEW IF EXISTS " + view); err != nil {
			return fmt.Errorf("drop %s view: %w", view, err)
		}
	}

	for _, table := range tables {
		query := fmt.Sprintf("ALTER TABLE __velocirepo_%s RENAME TO %s", table, table)
		if _, err := db.Exec(query); err != nil {
			return fmt.Errorf("rename %s table: %w", table, err)
		}
	}

	return nil
}

func createMetricsView(db *sql.DB, absDir string) error {
	query := metricsViewSQL(absDir)
	if _, err := db.Exec(query); err != nil {
		slog.Debug("metrics view creation failed, using empty view", "error", err)
		return createEmptyMetricsView(db)
	}
	return nil
}

func metricsViewSQL(absDir string) string {
	glob := filepath.ToSlash(filepath.Join(absDir, "metrics", "*", "*", "*.jsonl"))

	const eventsAgg = `SELECT
			project,
			source,
			target,
			CASE type
				WHEN 'star' THEN 'daily_stars'
				WHEN 'fork' THEN 'daily_forks'
				WHEN 'issue_open' THEN 'daily_issues_opened'
				WHEN 'issue_close' THEN 'daily_issues_closed'
				WHEN 'pr_open' THEN 'daily_prs_opened'
				WHEN 'pr_merge' THEN 'daily_prs_merged'
				WHEN 'comment' THEN 'daily_comments'
				ELSE 'daily_' || type
			END AS metric,
			CAST(datetime AS DATE) AS date,
			COUNT(*) AS value,
			NULL::JSON AS extra
		FROM events
		GROUP BY project, source, target, type, CAST(datetime AS DATE)`

	if !globHasMatches(glob) {
		return fmt.Sprintf(`CREATE OR REPLACE VIEW metrics AS %s`, eventsAgg)
	}

	return fmt.Sprintf(`CREATE OR REPLACE VIEW metrics AS
		SELECT
			project_id AS project,
			source,
			target,
			metric,
			CAST(date AS DATE) AS date,
			CAST(value AS BIGINT) AS value,
			extra
		FROM read_json('%s',
			format='newline_delimited',
			columns={source: 'VARCHAR', metric: 'VARCHAR', project_id: 'VARCHAR', target: 'VARCHAR', date: 'VARCHAR', value: 'BIGINT', extra: 'JSON'})
		UNION ALL
		%s`, escapeSQLString(glob), eventsAgg)
}

func createMetricsFilledView(db *sql.DB) error {
	query := `CREATE OR REPLACE VIEW metrics_filled AS
SELECT project, source, target, metric, date, value, extra
FROM (
    SELECT
        dates.project, dates.source, dates.target, dates.metric, dates.date, dates.extra,
        LAST_VALUE(m.value IGNORE NULLS) OVER (
            PARTITION BY dates.project, dates.source, dates.target, dates.metric, dates.extra
            ORDER BY dates.date
            ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
        ) AS value
    FROM (
        SELECT groups.project, groups.source, groups.target, groups.metric, groups.extra,
            UNNEST(generate_series(
                groups.min_date,
                groups.max_date,
                INTERVAL '1 day'
            ))::DATE AS date
        FROM (
            SELECT m.project, m.source, m.target, m.metric, m.extra,
                MIN(m.date) AS min_date,
                GREATEST(
                    MAX(m.date),
                    COALESCE(MAX(w.max_date), MAX(m.date))
                ) AS max_date
            FROM metrics m
            LEFT JOIN (
                SELECT project, source, target, MAX(date) AS max_date
                FROM __velocirepo_metric_watermarks
                WHERE target IS NOT NULL
                GROUP BY project, source, target
            ) w
                ON w.project = m.project
                AND w.source = m.source
                AND w.target = m.target
            WHERE m.metric LIKE 'total_%'
            GROUP BY m.project, m.source, m.target, m.metric, m.extra
        ) groups
    ) dates
    LEFT JOIN metrics m
        ON m.project = dates.project
        AND m.source = dates.source
        AND m.target = dates.target
        AND m.metric = dates.metric
        AND m.extra IS NOT DISTINCT FROM dates.extra
        AND m.date = dates.date
)
WHERE value IS NOT NULL
UNION ALL
SELECT * FROM metrics WHERE metric NOT LIKE 'total_%'`

	if _, err := db.Exec(query); err != nil {
		slog.Debug("metrics_filled view creation failed, using empty view", "error", err)
		return createEmptyMetricsFilledView(db)
	}
	return nil
}

func createMetricWatermarksView(db *sql.DB, absDir string) error {
	glob := filepath.ToSlash(filepath.Join(absDir, MetricsDir, "*", "*", WatermarkFileName))

	if !globHasMatches(glob) {
		return createEmptyMetricWatermarksView(db)
	}

	query := fmt.Sprintf(`CREATE OR REPLACE VIEW __velocirepo_metric_watermarks AS
		SELECT
			project_id AS project,
			source,
			target,
			CAST(date AS DATE) AS date
		FROM read_json('%s',
			format='newline_delimited',
			columns={source: 'VARCHAR', project_id: 'VARCHAR', target: 'VARCHAR', date: 'VARCHAR'})`,
		escapeSQLString(glob))

	if _, err := db.Exec(query); err != nil {
		slog.Debug("metric watermarks view creation failed, using empty view", "error", err)
		return createEmptyMetricWatermarksView(db)
	}
	return nil
}

func createEmptyMetricWatermarksView(db *sql.DB) error {
	_, err := db.Exec(`CREATE VIEW __velocirepo_metric_watermarks (project, source, target, date) AS
		SELECT NULL::VARCHAR, NULL::VARCHAR, NULL::VARCHAR, NULL::DATE
		WHERE false`)
	if err != nil {
		return fmt.Errorf("create empty metric watermarks view: %w", err)
	}
	return nil
}

func createEmptyMetricsFilledView(db *sql.DB) error {
	_, err := db.Exec(`CREATE VIEW metrics_filled (project, source, target, metric, date, value, extra) AS
		SELECT NULL::VARCHAR, NULL::VARCHAR, NULL::VARCHAR, NULL::VARCHAR, NULL::DATE, NULL::BIGINT, NULL::JSON
		WHERE false`)
	if err != nil {
		return fmt.Errorf("create empty metrics_filled view: %w", err)
	}
	return nil
}

func createEmptyMetricsView(db *sql.DB) error {
	_, err := db.Exec(`CREATE VIEW metrics (project, source, target, metric, date, value, extra) AS
		SELECT NULL::VARCHAR, NULL::VARCHAR, NULL::VARCHAR, NULL::VARCHAR, NULL::DATE, NULL::BIGINT, NULL::JSON
		WHERE false`)
	if err != nil {
		return fmt.Errorf("create empty metrics view: %w", err)
	}
	return nil
}

func createEventsView(db *sql.DB, absDir string) error {
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
			ref,
			"user",
			extra
		FROM read_json('%s',
			format='newline_delimited',
			columns={source: 'VARCHAR', type: 'VARCHAR', project_id: 'VARCHAR', target: 'VARCHAR', datetime: 'VARCHAR', ref: 'INTEGER', "user": 'VARCHAR', extra: 'JSON'})`,
		escapeSQLString(glob))

	if _, err := db.Exec(query); err != nil {
		slog.Debug("events view creation failed, using empty view", "error", err)
		return createEmptyEventsView(db)
	}
	return nil
}

func createEmptyEventsView(db *sql.DB) error {
	_, err := db.Exec(`CREATE VIEW events (project, source, type, target, datetime, ref, "user", extra) AS
		SELECT NULL::VARCHAR, NULL::VARCHAR, NULL::VARCHAR, NULL::VARCHAR, NULL::TIMESTAMP, NULL::INTEGER, NULL::VARCHAR, NULL::JSON
		WHERE false`)
	if err != nil {
		return fmt.Errorf("create empty events view: %w", err)
	}
	return nil
}

func createContentView(db *sql.DB, absDir string) error {
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
			ref,
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
			columns={project_id: 'VARCHAR', source: 'VARCHAR', target: 'VARCHAR', id: 'VARCHAR', ref: 'INTEGER', title: 'VARCHAR', description: 'VARCHAR', content: 'VARCHAR', published_at: 'VARCHAR', updated_at: 'VARCHAR', url: 'VARCHAR', duration: 'BIGINT', tags: 'JSON', type: 'VARCHAR', extra: 'JSON'})`,
		escapeSQLString(glob))

	if _, err := db.Exec(query); err != nil {
		slog.Debug("content view creation failed, using empty view", "error", err)
		return createEmptyContentView(db)
	}
	return nil
}

func createEmptyContentView(db *sql.DB) error {
	_, err := db.Exec(`CREATE VIEW content (project, source, target, id, ref, title, description, content, published_at, updated_at, url, duration, tags, type, extra) AS
		SELECT NULL::VARCHAR, NULL::VARCHAR, NULL::VARCHAR, NULL::VARCHAR, NULL::INTEGER, NULL::VARCHAR, NULL::VARCHAR, NULL::VARCHAR, NULL::TIMESTAMP, NULL::TIMESTAMP, NULL::VARCHAR, NULL::BIGINT, NULL::JSON, NULL::VARCHAR, NULL::JSON
		WHERE false`)
	if err != nil {
		return fmt.Errorf("create empty content view: %w", err)
	}
	return nil
}

func createProjectsView(db *sql.DB, projects []ProjectInfo) error {
	if len(projects) == 0 {
		_, err := db.Exec(`CREATE VIEW projects (id, name, description, color, tags, website, logo) AS
			SELECT NULL::VARCHAR, NULL::VARCHAR, NULL::VARCHAR, NULL::VARCHAR, NULL::VARCHAR[], NULL::VARCHAR, NULL::VARCHAR
			WHERE false`)
		if err != nil {
			return fmt.Errorf("create empty projects view: %w", err)
		}
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

	query := `CREATE OR REPLACE VIEW projects AS
		SELECT * FROM (VALUES ` + strings.Join(rows, ", ") + `) AS t(id, name, description, color, tags, website, logo)`

	if _, err := db.Exec(query); err != nil {
		return fmt.Errorf("create projects view: %w", err)
	}
	return nil
}

type IndicatorDef struct {
	Name        string
	Description string
	Query       string
}

var DefaultIndicators = []IndicatorDef{
	{
		Name:        "growth_rate",
		Description: "28-day growth rate (ratio of current vs prior 28-day sum)",
		Query: `SELECT project, source, target, metric,
	'{{indicator_name}}' AS indicator, date,
	(sum_28d - sum_prior_28d) / NULLIF(sum_prior_28d, 0.0) AS value,
	extra
FROM (
	SELECT *, SUM(value) OVER w AS sum_28d,
		SUM(value) OVER w_prior AS sum_prior_28d
	FROM metrics WHERE metric LIKE 'daily_%'
	WINDOW
		w AS (PARTITION BY project, source, target, metric, extra ORDER BY date ROWS BETWEEN 27 PRECEDING AND CURRENT ROW),
		w_prior AS (PARTITION BY project, source, target, metric, extra ORDER BY date ROWS BETWEEN 55 PRECEDING AND 28 PRECEDING)
) WHERE sum_prior_28d IS NOT NULL`,
	},
	{
		Name:        "trend",
		Description: "28-day linear trend (value per day via regression)",
		Query: `SELECT project, source, target, metric,
	'{{indicator_name}}' AS indicator, date,
	REGR_SLOPE(value, EXTRACT(EPOCH FROM CAST(date AS TIMESTAMP)) / 86400) OVER w AS value,
	extra
FROM metrics WHERE metric LIKE 'daily_%'
WINDOW w AS (PARTITION BY project, source, target, metric, extra ORDER BY date ROWS BETWEEN 27 PRECEDING AND CURRENT ROW)`,
	},
}

func createIndicatorsView(db *sql.DB, indicators []IndicatorDef) error {
	if len(indicators) == 0 {
		return createEmptyIndicatorsView(db)
	}

	var parts []string
	for _, ind := range indicators {
		q := strings.ReplaceAll(ind.Query, "{{indicator_name}}", escapeSQLString(ind.Name))
		parts = append(parts, q)
	}

	query := "CREATE OR REPLACE VIEW indicators AS " + strings.Join(parts, "\nUNION ALL\n")

	if _, err := db.Exec(query); err != nil {
		slog.Debug("indicators view creation failed, using empty view", "error", err)
		return createEmptyIndicatorsView(db)
	}
	return nil
}

func createEmptyIndicatorsView(db *sql.DB) error {
	_, err := db.Exec(`CREATE VIEW indicators (project, source, target, metric, indicator, date, value, extra) AS
		SELECT NULL::VARCHAR, NULL::VARCHAR, NULL::VARCHAR, NULL::VARCHAR, NULL::VARCHAR, NULL::DATE, NULL::DOUBLE, NULL::JSON
		WHERE false`)
	if err != nil {
		return fmt.Errorf("create empty indicators view: %w", err)
	}
	return nil
}

func escapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func SQLStringLiteral(s string) string {
	return "'" + escapeSQLString(s) + "'"
}

func queryRows(db *sql.DB, query string) ([]map[string]interface{}, []string, error) {
	rows, err := db.Query(query)
	if err != nil {
		return nil, nil, fmt.Errorf("query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}

	var results []map[string]interface{}
	for rows.Next() {
		values := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		row := make(map[string]interface{})
		for i, col := range cols {
			row[col] = values[i]
		}
		results = append(results, row)
	}
	return results, cols, rows.Err()
}
