package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/posit-dev/velocirepo/internal/sourceinfo"
)

type ExportOptions struct {
	DataDir    string
	OutDir     string
	Format     string
	Source     string
	Project    string
	Projects   []ProjectInfo
	Indicators []IndicatorDef
}

func Export(opts ExportOptions) ([]string, error) {
	db, err := openLiveDB(opts.DataDir, opts.Projects, opts.Indicators)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	absOut, err := filepath.Abs(opts.OutDir)
	if err != nil {
		return nil, fmt.Errorf("resolve output dir: %w", err)
	}

	if err := os.MkdirAll(absOut, 0755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	type table struct {
		name  string
		query string
	}

	tables := []table{
		{"metrics", buildExportQuery("metrics", opts)},
		{"events", buildExportQuery("events", opts)},
		{"content", buildExportQuery("content", opts)},
		{"indicators", buildExportQuery("indicators", opts)},
		{"projects", buildExportQuery("projects", opts)},
	}

	if opts.Source != "" {
		filtered := tables[:0]
		for _, t := range tables {
			if matchesSource(t.name, opts.Source) {
				filtered = append(filtered, t)
			}
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("no table matches source %q", opts.Source)
		}
		tables = filtered
	}

	var written []string
	for _, t := range tables {
		outFile := filepath.Join(absOut, t.name+"."+opts.Format)
		var copyFmt string
		switch opts.Format {
		case "parquet":
			copyFmt = "PARQUET"
		case "csv":
			copyFmt = "CSV, HEADER"
		default:
			return nil, fmt.Errorf("unsupported format %q (use parquet or csv)", opts.Format)
		}

		query := fmt.Sprintf(`COPY (%s) TO '%s' (FORMAT %s)`, t.query, escapeSQLString(outFile), copyFmt)
		if _, err := db.Exec(query); err != nil {
			return nil, fmt.Errorf("export %s: %w", t.name, err)
		}
		written = append(written, outFile)
	}

	return written, nil
}

func buildExportQuery(table string, opts ExportOptions) string {
	var conditions []string
	if opts.Project != "" {
		col := "project"
		if table == "projects" {
			col = "id"
		}
		if col != "" {
			conditions = append(conditions, fmt.Sprintf("%s = '%s'", col, escapeSQLString(opts.Project)))
		}
	}
	if opts.Source != "" && (table == "metrics" || table == "indicators" || table == "content") {
		conditions = append(conditions, fmt.Sprintf("source = '%s'", escapeSQLString(opts.Source)))
	}

	query := "SELECT * FROM " + table
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	switch table {
	case "metrics":
		query += " ORDER BY project, source, metric, date"
	case "events":
		query += " ORDER BY project, type, datetime"
	case "content":
		query += " ORDER BY source, target, published_at"
	case "indicators":
		query += " ORDER BY project, source, metric, indicator, date"
	case "projects":
		query += " ORDER BY id"
	}

	return query
}

func matchesSource(table, source string) bool {
	if source == "projects" {
		return table == "projects"
	}

	if d, ok := sourceinfo.Get(source); ok {
		if d.Category == sourceinfo.CategoryEvents {
			return table == "events"
		}
		if d.ContentDir != "" {
			return table == "metrics" || table == "content"
		}
		return table == "metrics" || table == "content"
	}

	return table == "metrics" || table == "content"
}
