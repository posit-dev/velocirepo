package mcp

import (
	"strings"
	"testing"
)

func TestValidateMCPQueryAllowsReadOnlyViewQueries(t *testing.T) {
	tests := []string{
		"SELECT COUNT(*) FROM metrics",
		"SELECT project, SUM(value) FROM metrics WHERE metric = 'daily_downloads' GROUP BY project",
		"WITH recent AS (SELECT * FROM metrics WHERE date > DATE '2025-01-01') SELECT COUNT(*) FROM recent",
		"SELECT e.type FROM events AS e JOIN projects p ON e.project = p.id",
		`SELECT "project" FROM "metrics"`,
		"SELECT * FROM main.metrics",
	}

	for _, query := range tests {
		t.Run(query, func(t *testing.T) {
			if _, err := validateMCPQuery(query); err != nil {
				t.Fatalf("validateMCPQuery() error = %v", err)
			}
		})
	}
}

func TestValidateMCPQueryRejectsUnsafeSQL(t *testing.T) {
	tests := []struct {
		query   string
		wantErr string
	}{
		{"SELECT content FROM read_text('/tmp/secret')", "read_text"},
		{"SELECT * FROM read_json('/tmp/data.json')", "read_json"},
		{"SELECT * FROM '/tmp/secret.csv'", "file paths"},
		{"SELECT * FROM metrics, '/tmp/secret.csv'", "file paths"},
		{"SELECT * FROM information_schema.tables", "schema"},
		{"COPY (SELECT * FROM metrics) TO '/tmp/out.parquet'", "SELECT or WITH"},
		{"SELECT * FROM metrics; SELECT * FROM events", "single SELECT"},
		{"WITH x AS (SELECT * FROM read_parquet('/tmp/x')) SELECT * FROM x", "read_parquet"},
		{"SELECT getenv('HOME')", "getenv"},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			_, err := validateMCPQuery(tt.query)
			if err == nil {
				t.Fatal("validateMCPQuery() error = nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateMCPQuery() error = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestPrepareMCPQueryAlwaysAddsOuterLimit(t *testing.T) {
	tests := []struct {
		name  string
		input string
		limit int
		want  string
	}{
		{
			name:  "simple query",
			input: "SELECT * FROM metrics",
			limit: 10,
			want:  "SELECT * FROM (SELECT * FROM metrics) AS velocirepo_mcp_query LIMIT 10",
		},
		{
			name:  "inner limit",
			input: "SELECT * FROM metrics LIMIT 5",
			limit: 10,
			want:  "SELECT * FROM (SELECT * FROM metrics LIMIT 5) AS velocirepo_mcp_query LIMIT 10",
		},
		{
			name:  "quoted limit identifier",
			input: `SELECT *, 1 AS "limit" FROM metrics`,
			limit: 10,
			want:  `SELECT * FROM (SELECT *, 1 AS "limit" FROM metrics) AS velocirepo_mcp_query LIMIT 10`,
		},
		{
			name:  "zero limit defaults",
			input: "SELECT * FROM metrics",
			limit: 0,
			want:  "SELECT * FROM (SELECT * FROM metrics) AS velocirepo_mcp_query LIMIT 1000",
		},
		{
			name:  "negative limit defaults",
			input: "SELECT * FROM metrics",
			limit: -5,
			want:  "SELECT * FROM (SELECT * FROM metrics) AS velocirepo_mcp_query LIMIT 1000",
		},
		{
			name:  "at maximum is not capped",
			input: "SELECT * FROM metrics",
			limit: 10000,
			want:  "SELECT * FROM (SELECT * FROM metrics) AS velocirepo_mcp_query LIMIT 10000",
		},
		{
			name:  "above maximum is capped",
			input: "SELECT * FROM metrics",
			limit: 99999,
			want:  "SELECT * FROM (SELECT * FROM metrics) AS velocirepo_mcp_query LIMIT 10000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, err := prepareMCPQuery(tt.input, tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			if query != tt.want {
				t.Fatalf("query = %q, want %q", query, tt.want)
			}
		})
	}
}

func TestValidateMCPQueryRejectsQueryTableFunctions(t *testing.T) {
	// query() and query_table() are DuckDB functions that can execute
	// arbitrary SQL strings, bypassing the guard. They must be forbidden.
	tests := []struct {
		query   string
		wantErr string
	}{
		{"SELECT * FROM query('SELECT * FROM metrics')", "query"},
		{"SELECT * FROM query_table('metrics')", "query_table"},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			_, err := validateMCPQuery(tt.query)
			if err == nil {
				t.Fatal("validateMCPQuery() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateMCPQuery() error = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}
