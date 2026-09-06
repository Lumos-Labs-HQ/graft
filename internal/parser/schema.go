package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/Lumos-Labs-HQ/flash/internal/config"
	"github.com/Lumos-Labs-HQ/flash/internal/utils"
)

var (
	createTableRegex    *regexp.Regexp
	enumRegex           *regexp.Regexp
	createKeyspaceRegex *regexp.Regexp
	createUDTRegex      *regexp.Regexp
)

func initRegex() {
	createTableRegex = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(\S+)\s*\(([\s\S]*?)\);`)
	enumRegex = regexp.MustCompile(`(?i)CREATE\s+TYPE\s+(\w+)\s+AS\s+ENUM\s*\(\s*([^)]+)\s*\)`)
	createKeyspaceRegex = regexp.MustCompile(`(?i)CREATE\s+KEYSPACE\s+(?:IF\s+NOT\s+EXISTS\s+)?(\S+)`)
	createUDTRegex = regexp.MustCompile(`(?i)CREATE\s+TYPE\s+(\S+)\s*\(([\s\S]*?)\);`)
}

var initRegexOnce = sync.OnceFunc(initRegex)

type SchemaParser struct {
	Config *config.Config
}

func NewSchemaParser(cfg *config.Config) *SchemaParser {
	initRegexOnce()
	return &SchemaParser{Config: cfg}
}

func (p *SchemaParser) Parse() (*Schema, error) {
	schema := &Schema{
		Tables: []*Table{},
		Enums:  []*Enum{},
	}

	schemaDir := p.Config.SchemaDir
	if !filepath.IsAbs(schemaDir) {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to get current working directory: %w", err)
		}
		schemaDir = filepath.Join(cwd, schemaDir)
	}

	if info, err := os.Stat(schemaDir); err == nil && info.IsDir() {
		files, _ := filepath.Glob(filepath.Join(schemaDir, "*.sql"))
		cqlFiles, _ := filepath.Glob(filepath.Join(schemaDir, "*.cql"))
		files = append(files, cqlFiles...)
		if len(files) > 0 {
			for _, file := range files {
				content, err := os.ReadFile(file)
				if err != nil {
					continue
				}

				if err := utils.ValidateSchemaSyntax(string(content), file); err != nil {
					return nil, err
				}

				tables := p.parseCreateTables(string(content))
				schema.Tables = append(schema.Tables, tables...)
				views := p.parseCreateViews(string(content))
				// Resolve wildcard views and skip those with no columns (unresolvable SELECT *)
				for _, v := range views {
					if len(v.Columns) == 1 && v.Columns[0].Name == "*" {
						continue // skip unresolvable wildcard views
					}
					// Skip if already added (dedup between tables and views)
					dup := false
					for _, t := range schema.Tables {
						if strings.EqualFold(t.Name, v.Name) {
							dup = true
							break
						}
					}
					if !dup {
						schema.Tables = append(schema.Tables, v)
					}
				}
				enums := p.parseCreateEnums(string(content))
				schema.Enums = append(schema.Enums, enums...)
				udts := p.parseCreateUDTs(string(content))
				schema.UDTs = append(schema.UDTs, udts...)
				if ks := p.parseCreateKeyspace(string(content)); ks != "" && schema.Keyspace == "" {
					schema.Keyspace = ks
				}
			}
			return schema, nil
		}
	}

	schemaPath := p.Config.SchemaPath
	if !filepath.IsAbs(schemaPath) {
		cwd, err := os.Getwd()
		if err != nil {
			return schema, fmt.Errorf("failed to get current working directory: %w", err)
		}
		schemaPath = filepath.Join(cwd, schemaPath)
	}

	if _, err := os.Stat(schemaPath); err == nil {
		content, err := os.ReadFile(schemaPath)
		if err != nil {
			return schema, nil
		}

		if err := utils.ValidateSchemaSyntax(string(content), schemaPath); err != nil {
			return nil, err
		}

		tables := p.parseCreateTables(string(content))
		schema.Tables = append(schema.Tables, tables...)
		views := p.parseCreateViews(string(content))
		for _, v := range views {
			if len(v.Columns) == 1 && v.Columns[0].Name == "*" {
				continue
			}
			dup := false
			for _, t := range schema.Tables {
				if strings.EqualFold(t.Name, v.Name) {
					dup = true
					break
				}
			}
			if !dup {
				schema.Tables = append(schema.Tables, v)
			}
		}
		enums := p.parseCreateEnums(string(content))
		schema.Enums = append(schema.Enums, enums...)
		udts := p.parseCreateUDTs(string(content))
		schema.UDTs = append(schema.UDTs, udts...)
		if ks := p.parseCreateKeyspace(string(content)); ks != "" && schema.Keyspace == "" {
			schema.Keyspace = ks
		}
	}

	return schema, nil
}

func (p *SchemaParser) parseCreateTables(sql string) []*Table {
	sql = utils.RemoveComments(sql)

	tables := make([]*Table, 0, 8)
	matches := createTableRegex.FindAllStringSubmatch(sql, -1)

	for _, match := range matches {
		if len(match) < 3 {
			continue
		}

		table := &Table{
			Name:    stripTableNameQuotes(match[1]),
			Columns: make([]*Column, 0, 16),
		}

		body := match[2]
		// Strip CQL WITH clause — may follow after a newline/space
		withStripper := regexp.MustCompile(`(?i)\)?\s*WITH\s+(CLUSTERING|COMPACT|compression)[\s\S]*$`)
		body = withStripper.ReplaceAllString(body, "")

		lines := utils.SplitColumns(body)
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			lineUpper := strings.ToUpper(line)
			if strings.HasPrefix(lineUpper, "PRIMARY") ||
				strings.HasPrefix(lineUpper, "FOREIGN") ||
				strings.HasPrefix(lineUpper, "UNIQUE") ||
				strings.HasPrefix(lineUpper, "CHECK") ||
				strings.HasPrefix(lineUpper, "CONSTRAINT") ||
				strings.HasPrefix(lineUpper, "INDEX") ||
				strings.HasPrefix(lineUpper, "KEY") ||
				strings.HasPrefix(lineUpper, "STATIC") {
				continue
			}

			// Extract column name and type properly, handling types with parentheses
			var colName, colType string

			// Find first space to get column name
			spaceIdx := strings.IndexAny(line, " \t")
			if spaceIdx == -1 || spaceIdx+1 >= len(line) {
				continue
			}

			colName = stripIdentifierQuotes(line[:spaceIdx])
			rest := strings.TrimSpace(line[spaceIdx+1:])

			// Extract type (handle parens + angle brackets for CQL types)
			parenDepth := 0
			angleDepth := 0
			typeEnd := 0
			for i, ch := range rest {
				switch ch {
				case '(':
					parenDepth++
				case ')':
					parenDepth--
				case '<':
					angleDepth++
				case '>':
					angleDepth--
				case ' ', '\t':
					if parenDepth <= 0 && angleDepth <= 0 {
						typeEnd = i
						goto typeEndFound
					}
				}
			}
		typeEndFound:
			if typeEnd == 0 {
				typeEnd = len(rest)
			}

			colType = rest[:typeEnd]

			isNullable := !strings.Contains(lineUpper, "NOT NULL") &&
				!strings.Contains(lineUpper, "PRIMARY KEY") &&
				!strings.Contains(strings.ToUpper(colType), "SERIAL")

			table.Columns = append(table.Columns, &Column{
				Name:     colName,
				Type:     colType,
				Nullable: isNullable,
			})
		}

		if len(table.Columns) > 0 {
			tables = append(tables, table)
		}
	}

	return tables
}

func (p *SchemaParser) parseCreateViews(sql string) []*Table {
	sql = utils.RemoveComments(sql)

	views := make([]*Table, 0, 4)
	viewNameRegex := regexp.MustCompile(`(?i)CREATE\s+(?:MATERIALIZED\s+)?VIEW\s+(?:IF\s+NOT\s+EXISTS\s+)?(\S+)\s+AS\s+SELECT\s+`)
	nameMatches := viewNameRegex.FindAllStringSubmatchIndex(sql, -1)

	for _, loc := range nameMatches {
		name := stripTableNameQuotes(sql[loc[2]:loc[3]])
		afterSelect := sql[loc[1]:]
		// Scope to the current statement only (up to the next top-level semicolon)
		stmtEnd := findTopLevelSemicolon(afterSelect)
		if stmtEnd != -1 {
			afterSelect = afterSelect[:stmtEnd]
		}
		selectList := extractTopLevelSelectList(afterSelect)
		cols := parseViewSelectColumns(selectList)
		if len(cols) == 0 {
			cols = []*Column{{Name: "*", Type: "text", Nullable: true}}
		}
		views = append(views, &Table{Name: name, Columns: cols})
	}
	return views
}

// findTopLevelSemicolon returns the index of the first `;` not inside parentheses.
func findTopLevelSemicolon(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ';':
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// extractTopLevelSelectList finds the select column list by scanning for the
// last top-level FROM (not inside parentheses).
func extractTopLevelSelectList(afterSelect string) string {
	upper := strings.ToUpper(afterSelect)
	depth := 0
	lastFrom := -1
	for i := 0; i < len(upper); i++ {
		switch upper[i] {
		case '(':
			depth++
		case ')':
			depth--
		default:
			if depth == 0 && strings.HasPrefix(upper[i:], "FROM ") {
				lastFrom = i
			}
		}
	}
	if lastFrom == -1 {
		return afterSelect
	}
	return strings.TrimSpace(afterSelect[:lastFrom])
}

// parseViewSelectColumns extracts named columns from a SELECT column list.
// Handles: bare names, aliases (expr AS alias), qualified names (t.col).
// Returns nil if it looks like SELECT * (wildcard).
func parseViewSelectColumns(selectList string) []*Column {
	selectList = strings.TrimSpace(selectList)
	if selectList == "*" {
		return nil
	}

	parts := utils.SplitColumns(selectList)
	cols := make([]*Column, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "*" {
			continue
		}
		name := extractSelectColumnName(part)
		if name == "" || name == "*" {
			continue
		}
		cols = append(cols, &Column{Name: name, Type: "text", Nullable: true})
	}
	return cols
}

// extractSelectColumnName returns the output column name for a SELECT expression.
// Handles: "col", "t.col", "expr AS alias", "expr alias".
func extractSelectColumnName(expr string) string {
	upper := strings.ToUpper(expr)
	// Check for AS alias
	if idx := strings.LastIndex(upper, " AS "); idx != -1 {
		return strings.Trim(strings.TrimSpace(expr[idx+4:]), `"`)
	}
	// Last word after space (implicit alias)
	// But only if it doesn't look like a function call — heuristic: no parens in last token
	parts := strings.Fields(expr)
	if len(parts) > 1 {
		last := parts[len(parts)-1]
		if !strings.Contains(last, "(") && !strings.Contains(last, ")") {
			return strings.Trim(last, `"`)
		}
	}
	// Qualified name: t.col → col
	if idx := strings.LastIndex(expr, "."); idx != -1 {
		return strings.Trim(expr[idx+1:], `"`)
	}
	return strings.Trim(expr, `"`)
}

func (p *SchemaParser) parseCreateEnums(sql string) []*Enum {
	sql = utils.RemoveComments(sql)

	enums := make([]*Enum, 0, 8) // Pre-allocate with reasonable capacity
	matches := enumRegex.FindAllStringSubmatch(sql, -1)

	for _, match := range matches {
		if len(match) < 3 {
			continue
		}

		enumName := match[1]
		valuesStr := match[2]

		var values []string
		for v := range strings.SplitSeq(valuesStr, ",") {
			v = strings.TrimSpace(v)
			v = strings.Trim(v, "'\"")
			if v != "" {
				values = append(values, v)
			}
		}

		if len(values) > 0 {
			enums = append(enums, &Enum{
				Name:   enumName,
				Values: values,
			})
		}
	}

	return enums
}

func (p *SchemaParser) parseCreateUDTs(sql string) []*UDT {
	sql = utils.RemoveComments(sql)
	matches := createUDTRegex.FindAllStringSubmatch(sql, -1)

	udts := make([]*UDT, 0, len(matches))
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		udt := &UDT{
			Name:   match[1],
			Fields: make([]*UDTField, 0),
		}
		// Parse fields: each is "name type" separated by commas
		for field := range strings.SplitSeq(match[2], ",") {
			field = strings.TrimSpace(field)
			parts := strings.SplitN(field, " ", 2)
			if len(parts) == 2 {
				udt.Fields = append(udt.Fields, &UDTField{Name: parts[0], Type: parts[1]})
			}
		}
		udts = append(udts, udt)
	}
	return udts
}

func (p *SchemaParser) parseCreateKeyspace(sql string) string {
	match := createKeyspaceRegex.FindStringSubmatch(sql)
	if len(match) >= 2 {
		return match[1]
	}
	return ""
}

func stripTableNameQuotes(name string) string {
	name = strings.TrimSpace(name)
	parts := strings.Split(name, ".")
	for i, part := range parts {
		parts[i] = stripIdentifierQuotes(part)
	}
	return strings.Join(parts, ".")
}

func stripIdentifierQuotes(name string) string {
	name = strings.TrimSpace(name)
	if len(name) >= 2 {
		switch {
		case name[0] == '"' && name[len(name)-1] == '"':
			return strings.ReplaceAll(name[1:len(name)-1], `""`, `"`)
		case name[0] == '`' && name[len(name)-1] == '`':
			return strings.ReplaceAll(name[1:len(name)-1], "``", "`")
		case name[0] == '[' && name[len(name)-1] == ']':
			return name[1 : len(name)-1]
		}
	}
	return name
}
