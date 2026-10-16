package mcp

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type sqlTokenKind int

const (
	sqlTokenIdent sqlTokenKind = iota
	sqlTokenString
	sqlTokenNumber
	sqlTokenSymbol
)

type sqlToken struct {
	kind sqlTokenKind
	text string
}

var mcpAllowedTables = map[string]bool{
	"content":    true,
	"events":     true,
	"indicators": true,
	"metrics":    true,
	"projects":   true,
}

var mcpForbiddenKeywords = map[string]bool{
	"alter":      true,
	"attach":     true,
	"call":       true,
	"checkpoint": true,
	"copy":       true,
	"create":     true,
	"delete":     true,
	"detach":     true,
	"drop":       true,
	"execute":    true,
	"export":     true,
	"force":      true,
	"import":     true,
	"insert":     true,
	"install":    true,
	"load":       true,
	"merge":      true,
	"pragma":     true,
	"reset":      true,
	"set":        true,
	"truncate":   true,
	"update":     true,
	"vacuum":     true,
}

var mcpForbiddenFunctions = map[string]bool{
	"current_setting": true,
	"duckdb_secrets":  true,
	"duckdb_settings": true,
	"getenv":          true,
	"glob":            true,
	"http_get":        true,
	"http_post":       true,
	"query":           true,
	"query_table":     true,
	"read_blob":       true,
	"read_csv":        true,
	"read_csv_auto":   true,
	"read_json":       true,
	"read_json_auto":  true,
	"read_ndjson":     true,
	"read_parquet":    true,
	"read_text":       true,
}

const defaultMCPQueryLimit = 1000

// maxMCPQueryLimit is the hard ceiling on the limit parameter accepted by
// prepareMCPQuery. Callers requesting more rows are silently capped here.
const maxMCPQueryLimit = 10000

func prepareMCPQuery(query string, limit int) (string, error) {
	if _, err := validateMCPQuery(query); err != nil {
		return "", err
	}
	switch {
	case limit <= 0:
		limit = defaultMCPQueryLimit
	case limit > maxMCPQueryLimit:
		limit = maxMCPQueryLimit
	}
	return fmt.Sprintf("SELECT * FROM (%s) AS velocirepo_mcp_query LIMIT %d", query, limit), nil
}

func validateMCPQuery(query string) ([]sqlToken, error) {
	tokens, err := tokenizeSQL(query)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("query is empty")
	}
	if !tokenIdentEquals(tokens[0], "select") && !tokenIdentEquals(tokens[0], "with") {
		return nil, fmt.Errorf("MCP query must be a SELECT or WITH query")
	}
	if err := validateBalancedParens(tokens); err != nil {
		return nil, err
	}

	for i, tok := range tokens {
		if tok.kind == sqlTokenSymbol && tok.text == ";" {
			return nil, fmt.Errorf("MCP query must contain a single SELECT statement")
		}
		if tok.kind != sqlTokenIdent {
			continue
		}

		name := strings.ToLower(tok.text)
		if mcpForbiddenKeywords[name] {
			return nil, fmt.Errorf("MCP query cannot use %s", strings.ToUpper(name))
		}
		if isForbiddenMCPFunction(name) && nextTokenIsSymbol(tokens, i, "(") {
			return nil, fmt.Errorf("MCP query cannot call %s", name)
		}
	}

	ctes := collectMCPCTENames(tokens)
	if err := validateMCPTableRefs(tokens, ctes); err != nil {
		return nil, err
	}

	return tokens, nil
}

func tokenizeSQL(query string) ([]sqlToken, error) {
	scanner := sqlScanner{query: query}
	return scanner.scan()
}

type sqlScanner struct {
	query  string
	pos    int
	tokens []sqlToken
}

func (s *sqlScanner) scan() ([]sqlToken, error) {
	for s.pos < len(s.query) {
		r, size := utf8.DecodeRuneInString(s.query[s.pos:])
		if r == utf8.RuneError && size == 1 {
			return nil, fmt.Errorf("invalid UTF-8 in query")
		}
		if unicode.IsSpace(r) {
			s.pos += size
			continue
		}

		if r == '-' && s.pos+1 < len(s.query) && s.query[s.pos+1] == '-' {
			s.pos += 2
			for s.pos < len(s.query) && s.query[s.pos] != '\n' {
				s.pos++
			}
			continue
		}
		if r == '/' && s.pos+1 < len(s.query) && s.query[s.pos+1] == '*' {
			end := strings.Index(s.query[s.pos+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("unterminated block comment")
			}
			s.pos += end + 4
			continue
		}
		if r == '\'' {
			next, err := scanQuoted(s.query, s.pos, '\'')
			if err != nil {
				return nil, err
			}
			s.append(sqlTokenString, s.query[s.pos:next])
			s.pos = next
			continue
		}
		if r == '"' {
			next, text, err := scanQuotedIdentifier(s.query, s.pos)
			if err != nil {
				return nil, err
			}
			s.append(sqlTokenIdent, text)
			s.pos = next
			continue
		}
		if isSQLIdentStart(r) {
			start := s.pos
			s.pos += size
			for s.pos < len(s.query) {
				next, nextSize := utf8.DecodeRuneInString(s.query[s.pos:])
				if !isSQLIdentPart(next) {
					break
				}
				s.pos += nextSize
			}
			s.append(sqlTokenIdent, s.query[start:s.pos])
			continue
		}
		if unicode.IsDigit(r) {
			start := s.pos
			s.pos += size
			for s.pos < len(s.query) {
				next, nextSize := utf8.DecodeRuneInString(s.query[s.pos:])
				if !unicode.IsDigit(next) && next != '.' {
					break
				}
				s.pos += nextSize
			}
			s.append(sqlTokenNumber, s.query[start:s.pos])
			continue
		}

		s.append(sqlTokenSymbol, s.query[s.pos:s.pos+size])
		s.pos += size
	}
	return s.tokens, nil
}

func (s *sqlScanner) append(kind sqlTokenKind, text string) {
	s.tokens = append(s.tokens, sqlToken{kind: kind, text: text})
}

func scanQuoted(query string, start int, quote byte) (int, error) {
	for i := start + 1; i < len(query); i++ {
		if query[i] != quote {
			continue
		}
		if i+1 < len(query) && query[i+1] == quote {
			i++
			continue
		}
		return i + 1, nil
	}
	return 0, fmt.Errorf("unterminated quoted value")
}

func scanQuotedIdentifier(query string, start int) (int, string, error) {
	var b strings.Builder
	for i := start + 1; i < len(query); i++ {
		if query[i] != '"' {
			b.WriteByte(query[i])
			continue
		}
		if i+1 < len(query) && query[i+1] == '"' {
			b.WriteByte('"')
			i++
			continue
		}
		return i + 1, b.String(), nil
	}
	return 0, "", fmt.Errorf("unterminated quoted identifier")
}

func isSQLIdentStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

func isSQLIdentPart(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func validateBalancedParens(tokens []sqlToken) error {
	depth := 0
	for _, tok := range tokens {
		if tok.kind != sqlTokenSymbol {
			continue
		}
		switch tok.text {
		case "(":
			depth++
		case ")":
			depth--
			if depth < 0 {
				return fmt.Errorf("unbalanced parentheses")
			}
		}
	}
	if depth != 0 {
		return fmt.Errorf("unbalanced parentheses")
	}
	return nil
}

func isForbiddenMCPFunction(name string) bool {
	return mcpForbiddenFunctions[name] ||
		strings.HasPrefix(name, "read_") ||
		strings.HasSuffix(name, "_scan")
}

func nextTokenIsSymbol(tokens []sqlToken, i int, symbol string) bool {
	return i+1 < len(tokens) && tokens[i+1].kind == sqlTokenSymbol && tokens[i+1].text == symbol
}

func tokenIdentEquals(tok sqlToken, value string) bool {
	return tok.kind == sqlTokenIdent && strings.EqualFold(tok.text, value)
}

func collectMCPCTENames(tokens []sqlToken) map[string]bool {
	ctes := make(map[string]bool)
	if len(tokens) == 0 || !tokenIdentEquals(tokens[0], "with") {
		return ctes
	}

	i := 1
	if i < len(tokens) && tokenIdentEquals(tokens[i], "recursive") {
		i++
	}
	for i < len(tokens) {
		if tokens[i].kind != sqlTokenIdent {
			return ctes
		}
		ctes[strings.ToLower(tokens[i].text)] = true
		i++

		if i < len(tokens) && tokens[i].kind == sqlTokenSymbol && tokens[i].text == "(" {
			i = skipBalanced(tokens, i)
		}
		if i >= len(tokens) || !tokenIdentEquals(tokens[i], "as") {
			return ctes
		}
		i++
		if i >= len(tokens) || tokens[i].kind != sqlTokenSymbol || tokens[i].text != "(" {
			return ctes
		}
		i = skipBalanced(tokens, i)
		if i < len(tokens) && tokens[i].kind == sqlTokenSymbol && tokens[i].text == "," {
			i++
			continue
		}
		return ctes
	}
	return ctes
}

func skipBalanced(tokens []sqlToken, start int) int {
	depth := 0
	for i := start; i < len(tokens); i++ {
		if tokens[i].kind != sqlTokenSymbol {
			continue
		}
		switch tokens[i].text {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(tokens)
}

func validateMCPTableRefs(tokens []sqlToken, ctes map[string]bool) error {
	validator := mcpTableRefValidator{
		tokens:     tokens,
		ctes:       ctes,
		inFromList: make(map[int]bool),
	}
	return validator.validate()
}

type mcpTableRefValidator struct {
	tokens     []sqlToken
	ctes       map[string]bool
	depth      int
	inFromList map[int]bool
}

func (v *mcpTableRefValidator) validate() error {
	for i, tok := range v.tokens {
		if tok.kind == sqlTokenSymbol {
			if err := v.handleSymbol(tok.text, i); err != nil {
				return err
			}
			continue
		}
		if tok.kind != sqlTokenIdent {
			continue
		}

		name := strings.ToLower(tok.text)
		if isMCPClauseEnd(name) {
			v.inFromList[v.depth] = false
			continue
		}
		if name == "from" || name == "join" {
			if err := v.validateRelation(i + 1); err != nil {
				return err
			}
			v.inFromList[v.depth] = true
		}
	}
	return nil
}

func (v *mcpTableRefValidator) handleSymbol(symbol string, index int) error {
	switch symbol {
	case "(":
		v.depth++
	case ")":
		delete(v.inFromList, v.depth)
		if v.depth > 0 {
			v.depth--
		}
	case ",":
		if v.inFromList[v.depth] {
			return v.validateRelation(index + 1)
		}
	}
	return nil
}

func (v *mcpTableRefValidator) validateRelation(start int) error {
	tokens := v.tokens
	i := start
	for i < len(tokens) && tokens[i].kind == sqlTokenIdent {
		name := strings.ToLower(tokens[i].text)
		if name != "lateral" && name != "only" {
			break
		}
		i++
	}
	if i >= len(tokens) {
		return fmt.Errorf("MCP query has an incomplete table reference")
	}
	if tokens[i].kind == sqlTokenSymbol && tokens[i].text == "(" {
		return nil
	}
	if tokens[i].kind == sqlTokenString {
		return fmt.Errorf("MCP query cannot read tables from file paths")
	}
	if tokens[i].kind != sqlTokenIdent {
		return fmt.Errorf("MCP query can only read metrics, events, content, projects, indicators, or CTEs")
	}

	name := strings.ToLower(tokens[i].text)
	if i+2 < len(tokens) &&
		tokens[i+1].kind == sqlTokenSymbol && tokens[i+1].text == "." &&
		tokens[i+2].kind == sqlTokenIdent {
		schema := name
		name = strings.ToLower(tokens[i+2].text)
		if schema != "main" && schema != "temp" {
			return fmt.Errorf("MCP query cannot read from schema %q", schema)
		}
		i += 2
	}

	if i+1 < len(tokens) && tokens[i+1].kind == sqlTokenSymbol && tokens[i+1].text == "(" {
		return fmt.Errorf("MCP query cannot read from table functions")
	}
	if !mcpAllowedTables[name] && !v.ctes[name] {
		return fmt.Errorf("MCP query cannot read from table %q", name)
	}
	return nil
}

func isMCPClauseEnd(name string) bool {
	switch name {
	case "except", "fetch", "group", "having", "intersect", "limit", "minus", "offset", "order", "qualify", "sample", "union", "where", "window":
		return true
	default:
		return false
	}
}
