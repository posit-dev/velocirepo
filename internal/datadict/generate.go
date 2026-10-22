package datadict

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/sourceinfo"
	"github.com/posit-dev/velocirepo/internal/store"
)

// Origin is the code that produced the data.
const Origin = "https://github.com/posit-dev/velocirepo"

// Options are what the dictionary describes besides the exported data.
type Options struct {
	// Config is the instance's config: its [data_dict] section, project
	// notes and shared targets. It may be nil.
	Config     *config.Config
	Indicators []store.IndicatorDef
	// Source and Project are the export's filters.
	Source  string
	Project string
}

// enumColumns are the columns with values from a known set, per table.
var enumColumns = map[string]map[string]bool{
	"targets":        {"source": true},
	"metrics":        {"source": true, "metric": true},
	"metrics_filled": {"source": true, "metric": true},
	"indicators":     {"source": true, "metric": true, "indicator": true},
	"events":         {"source": true, "type": true},
	"content":        {"source": true, "type": true, "state": true},
}

// projectTables are the tables with project, source and target columns.
var projectTables = []string{"metrics", "metrics_filled", "indicators", "events", "content"}

type generator struct {
	x       *store.Exported
	opts    Options
	dd      config.DataDictConfig
	exports map[string]bool
	// sources holds the sources of every table's rows.
	sources  map[string][]string
	warnings []string
}

// Generate describes an export. The warnings are about what the config
// leaves open, such as a shared target without a keeper; the dictionary
// records them as todos too.
func Generate(x *store.Exported, opts Options) (*Dictionary, []string, error) {
	g := &generator{x: x, opts: opts, exports: map[string]bool{}, sources: map[string][]string{}}
	if opts.Config != nil {
		g.dd = opts.Config.DataDict
		if opts.Indicators == nil {
			g.opts.Indicators = opts.Config.IndicatorDefs()
		}
	}
	for _, t := range x.Tables {
		g.exports[t] = true
	}
	for _, t := range append([]string{"targets"}, projectTables...) {
		if !g.exports[t] {
			continue
		}
		list, err := g.strings(fmt.Sprintf("SELECT DISTINCT source FROM export.%s WHERE source IS NOT NULL ORDER BY 1", t))
		if err != nil {
			return nil, nil, err
		}
		g.sources[t] = list
	}

	d := &Dictionary{Version: SpecVersion, LearnMore: LearnMore, Origin: Origin}
	dedup, err := g.dedupExpr()
	if err != nil {
		return nil, nil, err
	}
	if todo := g.unresolvedTodo(); dedup != "" && todo != "" {
		g.warnings = append(g.warnings, todo)
	}
	for _, name := range x.Tables {
		t, err := g.table(name, dedup)
		if err != nil {
			return nil, nil, err
		}
		d.Tables = append(d.Tables, t)
	}
	if err := g.constraints(d); err != nil {
		return nil, nil, err
	}
	if err := g.applyNotes(d); err != nil {
		return nil, nil, err
	}
	if err := g.dataset(d, dedup != ""); err != nil {
		return nil, nil, err
	}
	return d, g.warnings, nil
}

func (g *generator) table(name, dedup string) (Table, error) {
	t := Table{Name: name, Description: tableDescriptions[name], Details: tableDetails[name]}

	cols, err := g.columns(name)
	if err != nil {
		return t, err
	}
	extras := map[string]source.ColumnInfo{}
	for _, c := range g.x.Columns[name] {
		extras[c.Key] = c
	}
	for _, c := range cols {
		col, err := g.column(name, c[0], c[1], extras)
		if err != nil {
			return t, err
		}
		t.Columns = append(t.Columns, col)
	}

	switch name {
	case "metrics":
		coverage, err := g.coverage()
		if err != nil {
			return t, err
		}
		t.Details = joinText(t.Details, coverage)
		if _, ok := extras["ref"]; ok {
			t.Details = joinText(t.Details, "A total_reactions row's ref is an issue or pull request number: join it to content.ref on project, source and target too, since numbers repeat across repositories.")
		}
	case "events":
		t.Details = joinText(t.Details, "Join ref to content.ref on project, source and target too for the issue or pull request an event happened on; numbers repeat across repositories.")
	}

	if dedup != "" && contains(projectTables, name) {
		def := Definition{
			Name:        "deduplicated",
			Label:       "apply to any cross-project aggregate",
			Description: "Counts every target once: some targets are tracked by more than one project, with the same values. Apply it to any total, ranking or count that spans projects.",
			Expr:        dedup,
		}
		def.Todo = g.unresolvedTodo()
		t.Definitions = append(t.Definitions, def)
	}
	if name == "metrics" {
		measures, err := g.measures(dedup != "")
		if err != nil {
			return t, err
		}
		t.Definitions = append(t.Definitions, measures...)
	}
	if name == "metrics_filled" {
		t.Definitions = append(t.Definitions, Definition{
			Name:        "net_increase",
			Label:       "growth of a total_* over a date range",
			Description: "How much a total_* value grew over the queried range: last minus first, as totals only grow. Constrain it to one metric and group by target (or video, post or issue); never sum total_* values over days.",
			Expr:        "MAX(value) - MIN(value)",
		})
	}

	for _, def := range g.dd.Definitions {
		if def.Table != name {
			continue
		}
		for _, c := range t.Columns {
			if c.Name == def.Name {
				return t, fmt.Errorf("data_dict.definitions: %s.%s has the name of a column", name, def.Name)
			}
		}
		d := Definition{
			Name: def.Name, Label: def.Label, Description: def.Description, Details: def.Details, Expr: strings.TrimSpace(def.Expr),
		}
		// A definition of the config replaces the one velocirepo writes.
		replaced := false
		for i, existing := range t.Definitions {
			if existing.Name == def.Name {
				t.Definitions[i] = d
				replaced = true
			}
		}
		if !replaced {
			t.Definitions = append(t.Definitions, d)
		}
	}
	return t, nil
}

// columns returns a table's columns and DuckDB types, in order.
func (g *generator) columns(table string) ([][2]string, error) {
	rows, err := g.x.DB.Query(`SELECT column_name, data_type FROM duckdb_columns()
		WHERE schema_name = 'export' AND table_name = ? ORDER BY column_index`, table)
	if err != nil {
		return nil, fmt.Errorf("read the columns of %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	var cols [][2]string
	for rows.Next() {
		var c [2]string
		if err := rows.Scan(&c[0], &c[1]); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

func (g *generator) column(table, name, dbType string, extras map[string]source.ColumnInfo) (Column, error) {
	col := Column{Name: name, Description: columnDescription(table, name)}
	if info, ok := extras[name]; ok {
		col.Description = upperFirst(info.Description) + "; NULL for other rows."
	}

	switch {
	case dbType == "BOOLEAN":
		col.Type = "boolean"
	case dbType == "DATE":
		col.Type = "date"
		r, err := g.dateRange(table, name, "strftime(%s, '%%Y-%%m-%%d')")
		if err != nil {
			return col, err
		}
		col.Range = r
	case strings.HasPrefix(dbType, "TIMESTAMP"):
		col.Type = "datetime"
		col.TimeZone = "UTC"
		r, err := g.dateRange(table, name, "strftime(%s, '%%Y-%%m-%%dT%%H:%%M:%%S')")
		if err != nil {
			return col, err
		}
		col.Range = r
	case isNumber(dbType):
		if name == "ref" {
			col.Type = "number(id)"
			ex, err := g.examples(table, name, false)
			if err != nil {
				return col, err
			}
			col.Examples = numberExamples(ex)
			break
		}
		col.Type = "number(quantity)"
		if name == "duration" {
			col.Units = "seconds"
		}
		r, err := g.numberRange(table, name, dbType)
		if err != nil {
			return col, err
		}
		col.Range = r
	case dbType == "VARCHAR[]":
		col.Type = "list(string)"
		ex, err := g.examples(table, name, true)
		if err != nil {
			return col, err
		}
		list := Examples{}
		for _, e := range ex {
			list = append(list, e)
		}
		col.Examples = &list
	case enumColumns[table][name]:
		col.Type = "enum"
		values, todo, err := g.enumValues(table, name)
		if err != nil {
			return col, err
		}
		if len(values) == 0 {
			// An enum needs a value; an empty table's column is a string.
			col.Type = "string"
			col.Examples = &Examples{}
			break
		}
		col.Values = &values
		col.Todo = todo
	default:
		col.Type = "string"
		ex, err := g.examples(table, name, false)
		if err != nil {
			return col, err
		}
		list := Examples{}
		for _, e := range ex {
			list = append(list, e)
		}
		col.Examples = &list
		if len(ex) == 0 {
			col.Description = strings.TrimSpace(col.Description + " Empty in this export.")
		}
	}
	return col, nil
}

func columnDescription(table, name string) string {
	if d, ok := columnDescriptions[table][name]; ok {
		return d
	}
	switch table {
	case "metrics_filled", "indicators":
		if d, ok := columnDescriptions["metrics"][name]; ok {
			return d
		}
	}
	return ""
}

func isNumber(dbType string) bool {
	switch dbType {
	case "TINYINT", "SMALLINT", "INTEGER", "BIGINT", "HUGEINT", "UTINYINT", "USMALLINT", "UINTEGER", "UBIGINT", "FLOAT", "DOUBLE":
		return true
	}
	return strings.HasPrefix(dbType, "DECIMAL")
}

// examples returns about five values evenly spaced over a column's sorted
// distinct values, or a list column's elements, long ones cut short.
func (g *generator) examples(table, name string, list bool) ([]string, error) {
	values := fmt.Sprintf("SELECT %[1]s AS v FROM export.%[2]s", quoteIdent(name), quoteIdent(table))
	if list {
		values = fmt.Sprintf("SELECT unnest(%[1]s) AS v FROM export.%[2]s", quoteIdent(name), quoteIdent(table))
	}
	query := fmt.Sprintf(`WITH d AS (
			SELECT DISTINCT CAST(v AS VARCHAR) AS v FROM (%s) WHERE v IS NOT NULL
		), r AS (
			SELECT v, row_number() OVER (ORDER BY v) - 1 AS i, count(*) OVER () AS n FROM d
		)
		SELECT v FROM r
		WHERE i IN (0, (n - 1) // 4, (n - 1) // 2, 3 * (n - 1) // 4, n - 1)
		ORDER BY i`, values)
	examples, err := g.strings(query)
	if err != nil {
		return nil, err
	}
	for i, v := range examples {
		examples[i] = shorten(v, 80)
	}
	return examples, nil
}

func shorten(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func numberExamples(values []string) *Examples {
	list := Examples{}
	for _, v := range values {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			list = append(list, n)
		}
	}
	return &list
}

// dateRange returns a temporal column's observed range, formatted with
// format; open bounds for an empty column.
func (g *generator) dateRange(table, name, format string) ([]any, error) {
	col := quoteIdent(name)
	query := fmt.Sprintf("SELECT "+format+", "+format+" FROM (SELECT MIN(%s) AS lo, MAX(%s) AS hi FROM export.%s)",
		"lo", "hi", col, col, quoteIdent(table))
	var lo, hi sql.NullString
	if err := g.x.DB.QueryRow(query).Scan(&lo, &hi); err != nil {
		return nil, fmt.Errorf("read the range of %s.%s: %w", table, name, err)
	}
	if !lo.Valid {
		return []any{math.Inf(-1), math.Inf(1)}, nil
	}
	return []any{lo.String, hi.String}, nil
}

func (g *generator) numberRange(table, name, dbType string) ([]any, error) {
	col := quoteIdent(name)
	var lo, hi sql.NullString
	query := fmt.Sprintf("SELECT CAST(MIN(%s) AS VARCHAR), CAST(MAX(%s) AS VARCHAR) FROM export.%s", col, col, quoteIdent(table))
	if err := g.x.DB.QueryRow(query).Scan(&lo, &hi); err != nil {
		return nil, fmt.Errorf("read the range of %s.%s: %w", table, name, err)
	}
	if !lo.Valid {
		return []any{math.Inf(-1), math.Inf(1)}, nil
	}
	parse := func(s string) any {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(f) {
			return s
		}
		return f
	}
	return []any{parse(lo.String), parse(hi.String)}, nil
}

// enumValues returns the values of an enum column with their labels, and a
// todo for values nothing describes.
func (g *generator) enumValues(table, name string) (Values, string, error) {
	query := fmt.Sprintf(`SELECT DISTINCT %s, source FROM export.%s WHERE %s IS NOT NULL ORDER BY 1, 2`, quoteIdent(name), quoteIdent(table), quoteIdent(name))
	if name == "source" {
		query = fmt.Sprintf(`SELECT DISTINCT source, source FROM export.%s WHERE source IS NOT NULL ORDER BY 1`, quoteIdent(table))
	}
	rows, err := g.x.DB.Query(query)
	if err != nil {
		return nil, "", fmt.Errorf("read the values of %s.%s: %w", table, name, err)
	}
	defer func() { _ = rows.Close() }()
	var order []string
	bySource := map[string][]string{}
	for rows.Next() {
		var value, src string
		if err := rows.Scan(&value, &src); err != nil {
			return nil, "", err
		}
		if _, ok := bySource[value]; !ok {
			order = append(order, value)
		}
		bySource[value] = append(bySource[value], src)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	family := table
	if table == "metrics_filled" || table == "indicators" {
		family = "metrics"
	}
	overrides := g.dd.Values[table][name]
	if overrides == nil {
		overrides = g.dd.Values[family][name]
	}
	var values Values
	var undescribed []string
	for _, v := range order {
		label := overrides[v]
		if label == "" {
			label = g.label(name, v, bySource[v])
		}
		if label == "" {
			undescribed = append(undescribed, v)
			label = "undocumented"
		}
		values = append(values, Value{Name: v, Label: label})
	}
	todo := ""
	if len(undescribed) > 0 {
		todo = fmt.Sprintf("Describe %s in [data_dict.values.%s.%s] of velocirepo.toml.", strings.Join(undescribed, ", "), table, name)
		g.warnings = append(g.warnings, fmt.Sprintf("%s.%s has values without a description: %s; describe them in [data_dict.values.%s.%s]", table, name, strings.Join(undescribed, ", "), table, name))
	}
	return values, todo, nil
}

// label describes a value of an enum column from the source metadata.
func (g *generator) label(column, value string, sources []string) string {
	switch column {
	case "source":
		meta, ok := source.MetaFor(value)
		d, known := sourceinfo.Get(value)
		if !ok || !known {
			return ""
		}
		return d.DisplayName + "; a target is " + meta.Target
	case "indicator":
		for _, ind := range g.opts.Indicators {
			if ind.Name == value {
				return ind.Description
			}
		}
		return ""
	case "state":
		return stateLabels[value]
	}

	var parts []string
	for _, src := range sources {
		meta, _ := source.MetaFor(src)
		desc := ""
		switch column {
		case "metric":
			for _, m := range meta.Metrics {
				if m.Name == value {
					desc = m.Description
				}
			}
		case "type":
			for _, e := range meta.Events {
				if e.Name == value {
					desc = e.Description
				}
			}
			for _, c := range meta.Content {
				if c.Name == value {
					desc = c.Description
				}
			}
		}
		if desc == "" {
			return ""
		}
		parts = append(parts, src+": "+desc)
	}
	if len(parts) == 1 {
		return strings.SplitN(parts[0], ": ", 2)[1] + " (" + sources[0] + ")"
	}
	return strings.Join(parts, "; ")
}

// coverage says per source how many targets the metrics cover, and since
// when.
func (g *generator) coverage() (string, error) {
	rows, err := g.x.DB.Query(`SELECT source, COUNT(DISTINCT target), strftime(MIN(date), '%Y-%m-%d'), strftime(MAX(date), '%Y-%m-%d')
		FROM export.metrics GROUP BY source ORDER BY source`)
	if err != nil {
		return "", fmt.Errorf("read the coverage of metrics: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var parts []string
	for rows.Next() {
		var src, from, through string
		var targets int
		if err := rows.Scan(&src, &targets, &from, &through); err != nil {
			return "", err
		}
		noun := "targets"
		if targets == 1 {
			noun = "target"
		}
		parts = append(parts, fmt.Sprintf("%s, %d %s, %s to %s", src, targets, noun, from, through))
	}
	if err := rows.Err(); err != nil || len(parts) == 0 {
		return "", err
	}
	return "Coverage: " + strings.Join(parts, "; ") + ".", nil
}

// measures are the governed sums of the daily metrics the export has.
func (g *generator) measures(dedup bool) ([]Definition, error) {
	rows, err := g.x.DB.Query("SELECT DISTINCT source, metric FROM export.metrics ORDER BY 1, 2")
	if err != nil {
		return nil, fmt.Errorf("read the metrics: %w", err)
	}
	defer func() { _ = rows.Close() }()
	present := map[[2]string]bool{}
	for rows.Next() {
		var src, metric string
		if err := rows.Scan(&src, &metric); err != nil {
			return nil, err
		}
		present[[2]string{src, metric}] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var defs []Definition
	for _, meta := range source.Metas() {
		for _, m := range meta.Metrics {
			if m.Measure == "" || !present[[2]string{meta.Name, m.Name}] {
				continue
			}
			cond := fmt.Sprintf("source = %s AND metric = %s", store.SQLStringLiteral(meta.Name), store.SQLStringLiteral(m.Name))
			desc := m.MeasureDescription
			if dedup {
				cond += " AND deduplicated"
				desc += " Counts every target once."
			}
			defs = append(defs, Definition{
				Name:        m.Measure,
				Description: desc,
				Expr:        "SUM(CASE WHEN " + cond + " THEN value ELSE 0 END)",
			})
		}
	}
	return defs, nil
}

// dedupExpr is the filter that leaves out the copies of shared targets, or
// "" when no target is shared.
func (g *generator) dedupExpr() (string, error) {
	if !g.exports["targets"] {
		return "", nil
	}
	rows, err := g.x.DB.Query(`SELECT project, source, target FROM export.targets WHERE NOT canonical ORDER BY project, source, target`)
	if err != nil {
		return "", fmt.Errorf("read the targets: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type copyRow struct{ project, source, target string }
	var copies []copyRow
	for rows.Next() {
		var c copyRow
		if err := rows.Scan(&c.project, &c.source, &c.target); err != nil {
			return "", err
		}
		copies = append(copies, c)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	// A project whose targets are all copies is left out as a whole.
	allCopies := map[string]bool{}
	counts, err := g.x.DB.Query(`SELECT project, bool_and(NOT canonical) FROM export.targets GROUP BY project`)
	if err != nil {
		return "", err
	}
	defer func() { _ = counts.Close() }()
	for counts.Next() {
		var p string
		var all bool
		if err := counts.Scan(&p, &all); err != nil {
			return "", err
		}
		allCopies[p] = all
	}
	if err := counts.Err(); err != nil {
		return "", err
	}

	var whole []string
	var clauses []string
	seen := map[string]bool{}
	for _, c := range copies {
		if allCopies[c.project] {
			if !seen[c.project] {
				seen[c.project] = true
				whole = append(whole, store.SQLStringLiteral(c.project))
			}
			continue
		}
		clauses = append(clauses, fmt.Sprintf("NOT (project = %s AND source = %s AND target = %s)",
			store.SQLStringLiteral(c.project), store.SQLStringLiteral(c.source), store.SQLStringLiteral(c.target)))
	}
	if len(whole) > 0 {
		clauses = append([]string{"project NOT IN (" + strings.Join(whole, ", ") + ")"}, clauses...)
	}
	return strings.Join(clauses, "\nAND "), nil
}

// unresolvedTodo lists the shared targets the config doesn't give a keeper.
func (g *generator) unresolvedTodo() string {
	if g.opts.Config == nil {
		return ""
	}
	var parts []string
	for _, st := range g.opts.Config.SharedTargets() {
		if st.Resolved || !g.inExport(st) {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %s (%s; counted under %s)", st.Source, st.Target, strings.Join(st.Projects, ", "), st.Keeper))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Say which project keeps these shared targets in [data_dict.keep.<source>] of velocirepo.toml, or mark copies with duplicate_of: " + strings.Join(parts, "; ") + "."
}

func (g *generator) inExport(st config.SharedTarget) bool {
	if g.opts.Source != "" && g.opts.Source != st.Source {
		return false
	}
	return g.opts.Project == "" || contains(st.Projects, g.opts.Project)
}

// constraints adds the keys, foreign keys and relationships the data
// satisfies.
func (g *generator) constraints(d *Dictionary) error {
	tables := map[string]*Table{}
	for i := range d.Tables {
		tables[d.Tables[i].Name] = &d.Tables[i]
	}
	addConstraint := func(t *Table, column, constraint string) {
		for i := range t.Columns {
			if t.Columns[i].Name == column {
				t.Columns[i].Constraints = append(t.Columns[i].Constraints, constraint)
			}
		}
	}
	unique := func(table string, columns ...string) (bool, error) {
		list := strings.Join(quoteIdents(columns), ", ")
		var dups, nulls int
		query := fmt.Sprintf(`SELECT
			(SELECT COUNT(*) FROM (SELECT %[1]s FROM export.%[2]s GROUP BY %[1]s HAVING COUNT(*) > 1)),
			(SELECT COUNT(*) FROM export.%[2]s WHERE %[3]s)`, list, quoteIdent(table), strings.Join(isNull(columns), " OR "))
		if err := g.x.DB.QueryRow(query).Scan(&dups, &nulls); err != nil {
			return false, fmt.Errorf("check the key of %s: %w", table, err)
		}
		return dups == 0 && nulls == 0, nil
	}

	keys := map[string][]string{
		"projects": {"id"},
		"targets":  {"project", "source", "target"},
		"content":  {"project", "source", "target", "id"},
	}
	keyed := map[string]bool{}
	for _, table := range []string{"projects", "targets", "content"} {
		t := tables[table]
		if t == nil {
			continue
		}
		ok, err := unique(table, keys[table]...)
		if err != nil {
			return err
		}
		if !ok {
			g.warnings = append(g.warnings, fmt.Sprintf("%s has rows with the same %s, so the dictionary declares no key", table, strings.Join(keys[table], ", ")))
			continue
		}
		keyed[table] = true
		for _, c := range keys[table] {
			addConstraint(t, c, "primary_key")
		}
	}

	if keyed["projects"] {
		for _, table := range append([]string{"targets"}, projectTables...) {
			t := tables[table]
			if t == nil {
				continue
			}
			orphans, err := g.strings(fmt.Sprintf(`SELECT DISTINCT project FROM export.%s
				WHERE project IS NOT NULL AND project NOT IN (SELECT id FROM export.projects) ORDER BY 1`, quoteIdent(table)))
			if err != nil {
				return err
			}
			rel := Relationship{Join: table + ".project = projects.id", Cardinality: "many-to-one"}
			if len(orphans) == 0 {
				addConstraint(t, "project", "foreign_key")
			} else {
				rel.Description = "Some projects of the data aren't in velocirepo.toml anymore: " + strings.Join(orphans, ", ") + "."
			}
			d.Relationships = append(d.Relationships, rel)
		}
	}
	if keyed["targets"] {
		for _, table := range projectTables {
			if tables[table] == nil {
				continue
			}
			d.Relationships = append(d.Relationships, Relationship{
				Join:        fmt.Sprintf("%[1]s.project = targets.project AND %[1]s.source = targets.source AND %[1]s.target = targets.target", table),
				Cardinality: "many-to-one",
				Description: "The target as configured; rows of targets no longer in velocirepo.toml have no match.",
			})
		}
	}
	if keyed["content"] {
		for _, table := range []string{"metrics", "metrics_filled"} {
			for _, c := range g.x.Columns[table] {
				if c.Identifies != "id" || tables[table] == nil {
					continue
				}
				d.Relationships = append(d.Relationships, Relationship{
					Join: fmt.Sprintf("%[1]s.project = content.project AND %[1]s.source = content.source AND %[1]s.target = content.target AND %[1]s.%[2]s = content.id",
						table, c.Key),
					Cardinality: "many-to-one",
					Description: fmt.Sprintf("Rows keyed by %s to the item in content.", c.Key),
				})
			}
		}
	}
	return nil
}

// applyNotes adds the config's notes to the tables and columns they're
// about.
func (g *generator) applyNotes(d *Dictionary) error {
	for i, n := range g.dd.Notes {
		text := strings.TrimSpace(n.Text)
		if n.Source != "" {
			text = sourceinfo.Must(n.Source).DisplayName + ": " + text
		}
		for _, name := range n.Tables {
			var t *Table
			for j := range d.Tables {
				if d.Tables[j].Name == name {
					t = &d.Tables[j]
				}
			}
			if t == nil {
				// Filtered out of this export.
				continue
			}
			if n.Column == "" {
				t.Details = joinText(t.Details, text)
				continue
			}
			found := false
			for j := range t.Columns {
				if t.Columns[j].Name == n.Column {
					t.Columns[j].Details = joinText(t.Columns[j].Details, text)
					found = true
				}
			}
			if !found {
				return fmt.Errorf("data_dict.notes[%d]: %s has no column %q in this export", i, name, n.Column)
			}
		}
	}
	return nil
}

// dataset describes the export as a whole, with the glossary.
func (g *generator) dataset(d *Dictionary, dedup bool) error {
	d.Name = g.dd.Name
	if d.Name == "" {
		d.Name = "velocirepo"
	}
	d.Label = g.dd.Label

	var asOf sql.NullString
	if g.exports["meta"] {
		if err := g.x.DB.QueryRow("SELECT strftime(as_of, '%Y-%m-%d') FROM export.meta").Scan(&asOf); err != nil {
			return fmt.Errorf("read meta: %w", err)
		}
	}
	if asOf.Valid {
		d.DataVersion = &Version{Date: asOf.String}
	}

	var sources []string
	seen := map[string]bool{}
	for _, t := range append([]string{"targets"}, projectTables...) {
		for _, s := range g.sources[t] {
			if !seen[s] {
				seen[s] = true
				sources = append(sources, s)
			}
		}
	}
	sort.Strings(sources)
	var names []string
	for _, s := range sources {
		if desc, ok := sourceinfo.Get(s); ok {
			names = append(names, desc.DisplayName)
		} else {
			names = append(names, s)
		}
	}

	var projects int
	if g.exports["projects"] {
		if err := g.x.DB.QueryRow("SELECT COUNT(*) FROM export.projects").Scan(&projects); err != nil {
			return err
		}
	}
	generated := fmt.Sprintf("Open source project metrics collected by velocirepo for %d projects", projects)
	if len(names) > 0 {
		generated += " from " + joinList(names)
	}
	if asOf.Valid {
		generated += ", through " + asOf.String
	}
	generated += "."
	var filters []string
	if g.opts.Source != "" {
		filters = append(filters, "source "+g.opts.Source)
	}
	if g.opts.Project != "" {
		filters = append(filters, "project "+g.opts.Project)
	}
	if len(filters) > 0 {
		generated += " Exported for " + strings.Join(filters, " and ") + " only."
	}
	d.Description = joinText(strings.TrimSpace(g.dd.Description), generated)

	details := datasetDetails
	if dedup {
		details = joinText(details, "Some targets are tracked by more than one project, with the same values: apply the deduplicated filter to any aggregate that spans projects.")
	}
	for _, s := range sources {
		meta, _ := source.MetaFor(s)
		if meta.Notes != "" {
			details = joinText(details, sourceinfo.Must(s).DisplayName+": "+meta.Notes)
		}
	}
	d.Details = joinText(details, strings.TrimSpace(g.dd.Details))

	gl := Glossary(append([]Value(nil), glossary...))
	if dedup {
		gl = append(gl, Value{"deduplicated", "The filter that counts every target once, where more than one project tracks a target; targets.canonical says which copy counts."})
	}
	if g.opts.Config != nil {
		var ids []string
		for id, p := range g.opts.Config.Projects {
			if p.Notes != "" && (g.opts.Project == "" || g.opts.Project == id) {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		for _, id := range ids {
			gl = append(gl, Value{id, strings.TrimSpace(g.opts.Config.Projects[id].Notes)})
		}
	}
	var terms []string
	for term := range g.dd.Glossary {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	for _, term := range terms {
		gl = append(gl, Value{term, strings.TrimSpace(g.dd.Glossary[term])})
	}
	d.Glossary = &gl
	return nil
}

func (g *generator) strings(query string) ([]string, error) {
	rows, err := g.x.DB.Query(query)
	if err != nil {
		return nil, fmt.Errorf("query %q: %w", query, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func quoteIdents(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = quoteIdent(s)
	}
	return out
}

func isNull(columns []string) []string {
	out := make([]string, len(columns))
	for i, c := range columns {
		out[i] = quoteIdent(c) + " IS NULL"
	}
	return out
}

func joinText(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n\n" + b
}

func joinList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
