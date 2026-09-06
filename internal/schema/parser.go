package schema

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

var (
	parserFKRegex         = regexp.MustCompile(`(?i)FOREIGN\s+KEY\s*\(\s*(\w+)\s*\)\s+REFERENCES\s+(\w+)\s*\(\s*(\w+)\s*\)(?:\s+ON\s+DELETE\s+(CASCADE|SET\s+NULL|RESTRICT|NO\s+ACTION))?(?:\s+ON\s+UPDATE\s+(CASCADE|SET\s+NULL|RESTRICT|NO\s+ACTION))?`)
	parserReferencesRegex = regexp.MustCompile(`(?i)REFERENCES\s+(\w+)\s*\(\s*(\w+)\s*\)`)
	parserOnDeleteRegex   = regexp.MustCompile(`(?i)ON\s+DELETE\s+(CASCADE|SET\s+NULL|RESTRICT|NO\s+ACTION)`)
	parserOnUpdateRegex   = regexp.MustCompile(`(?i)ON\s+UPDATE\s+(CASCADE|SET\s+NULL|RESTRICT|NO\s+ACTION)`)
	parserIdentityRegex   = regexp.MustCompile(`(?i)GENERATED\s+(?:ALWAYS|BY\s+DEFAULT)\s+AS\s+IDENTITY`)
)

func (sm *SchemaManager) cleanSQL(sql string) string {
	sql = commentRegex.ReplaceAllString(sql, "")
	return strings.TrimSpace(whitespaceRegex.ReplaceAllString(sql, " "))
}

func (sm *SchemaManager) splitStatements(sql string) []string {
	statements := strings.Split(sql, ";")
	result := make([]string, 0, len(statements))

	for _, stmt := range statements {
		if stmt = strings.TrimSpace(stmt); stmt != "" {
			result = append(result, stmt)
		}
	}
	return result
}

func (sm *SchemaManager) isCreateTableStatement(stmt string) bool {
	return createTableStmtRegex.MatchString(stmt)
}

func (sm *SchemaManager) isCreateIndexStatement(stmt string) bool {
	return createIndexStmtRegex.MatchString(stmt)
}

func (sm *SchemaManager) parseCreateIndexStatement(stmt string) (types.SchemaIndex, error) {
	whereClause := ""
	if whereMatch := indexWhereRegex.FindStringSubmatch(stmt); len(whereMatch) > 1 {
		whereClause = strings.TrimSpace(whereMatch[1])
		stmt = stmt[:strings.Index(strings.ToUpper(stmt), " WHERE")]
	}

	method := ""
	if usingMatch := indexUsingRegex.FindStringSubmatch(stmt); len(usingMatch) > 1 {
		method = strings.ToLower(usingMatch[1])
		stmt = indexUsingRegex.ReplaceAllString(stmt, "")
	}

	matches := indexRegex.FindStringSubmatch(stmt)
	if len(matches) < 5 {
		return types.SchemaIndex{}, fmt.Errorf("could not parse CREATE INDEX statement: %s", stmt)
	}

	isUnique := strings.TrimSpace(matches[1]) != ""
	indexName := stripNameQuotes(matches[2])
	tableName := stripNameQuotes(matches[3])
	columnsStr := matches[4]

	columnParts := strings.Split(columnsStr, ",")
	var columns []string
	var exprs []string
	for _, col := range columnParts {
		col = strings.TrimSpace(col)
		if strings.Contains(col, "(") {
			exprs = append(exprs, col)
			columns = append(columns, col)
			continue
		}
		col = strings.Trim(col, `"'`)
		col = indexOrderRegex.ReplaceAllString(col, "")
		col = strings.TrimSpace(col)
		if col != "" {
			columns = append(columns, col)
		}
	}

	return types.SchemaIndex{
		Name:    indexName,
		Table:   tableName,
		Columns: columns,
		Unique:  isUnique,
		Where:   whereClause,
		Method:  method,
		Expr:    exprs,
	}, nil
}

func stripNameQuotes(name string) string {
	name = strings.TrimSpace(name)
	if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
		name = name[1 : len(name)-1]
	}
	if len(name) >= 2 && name[0] == '`' && name[len(name)-1] == '`' {
		name = name[1 : len(name)-1]
	}
	return name
}

func (sm *SchemaManager) isCreateTypeStatement(stmt string) bool {
	return createTypeStmtRegex.MatchString(stmt)
}

func (sm *SchemaManager) isCreateViewStatement(stmt string) bool {
	return createViewStmtRegex.MatchString(stmt)
}

func (sm *SchemaManager) parseCreateViewStatement(stmt string) (string, string, error) {
	matches := createViewRegex.FindStringSubmatch(stmt)
	if len(matches) < 2 {
		return "", "", fmt.Errorf("could not parse CREATE MATERIALIZED VIEW: %s", stmt)
	}
	return stripNameQuotes(matches[1]), strings.TrimSpace(stmt), nil
}

func (sm *SchemaManager) isCreateUDTStatement(stmt string) bool {
	return createUDTStmtRegex.MatchString(stmt) && !createTypeStmtRegex.MatchString(stmt)
}

func (sm *SchemaManager) parseCreateUDTStatement(stmt string) (types.SchemaUDT, error) {
	matches := createUDTFullRegex.FindStringSubmatch(stmt)
	if len(matches) < 3 {
		return types.SchemaUDT{}, fmt.Errorf("could not parse CREATE TYPE (UDT): %s", stmt)
	}
	name := stripNameQuotes(matches[1])
	fieldsStr := matches[2]

	// Split fields by comma, respecting angle brackets
	var fields []types.SchemaUDTField
	var current strings.Builder
	angleLevel := 0
	for _, ch := range fieldsStr {
		switch ch {
		case '<':
			angleLevel++
			current.WriteRune(ch)
		case '>':
			angleLevel--
			current.WriteRune(ch)
		case ',':
			if angleLevel == 0 {
				field := strings.TrimSpace(current.String())
				if field != "" {
					if fld, err := sm.parseUDTField(field); err == nil {
						fields = append(fields, fld)
					}
				}
				current.Reset()
			} else {
				current.WriteRune(ch)
			}
		default:
			current.WriteRune(ch)
		}
	}
	if field := strings.TrimSpace(current.String()); field != "" {
		if fld, err := sm.parseUDTField(field); err == nil {
			fields = append(fields, fld)
		}
	}

	return types.SchemaUDT{Name: name, Fields: fields}, nil
}

func (sm *SchemaManager) parseUDTField(field string) (types.SchemaUDTField, error) {
	spaceIdx := strings.IndexAny(field, " \t")
	if spaceIdx == -1 {
		return types.SchemaUDTField{}, fmt.Errorf("invalid UDT field: %s", field)
	}
	return types.SchemaUDTField{
		Name: strings.TrimSpace(field[:spaceIdx]),
		Type: strings.TrimSpace(field[spaceIdx+1:]),
	}, nil
}

func (sm *SchemaManager) isCreateKeyspaceStatement(stmt string) bool {
	return createKeyspaceRegex.MatchString(stmt)
}

func (sm *SchemaManager) parseCreateKeyspaceStatement(stmt string) (types.SchemaKeyspace, error) {
	matches := createKeyspaceRegex.FindStringSubmatch(stmt)
	if len(matches) < 3 {
		return types.SchemaKeyspace{}, fmt.Errorf("could not parse CREATE KEYSPACE: %s", stmt)
	}
	ks := types.SchemaKeyspace{
		Name:        stripNameQuotes(matches[1]),
		Replication: strings.TrimSpace(matches[2]),
	}
	if len(matches) >= 4 && matches[3] != "" {
		dw := strings.EqualFold(matches[3], "true")
		ks.DurableWrites = &dw
	}
	return ks, nil
}

func (sm *SchemaManager) parseCreateTypeStatement(stmt string) (types.SchemaEnum, error) {
	matches := enumRegex.FindStringSubmatch(stmt)

	if len(matches) < 4 {
		return types.SchemaEnum{}, fmt.Errorf("could not parse CREATE TYPE statement: %s", stmt)
	}

	// Extract enum name
	enumName := matches[1]
	if enumName == "" {
		enumName = matches[2]
	}

	// Extract values
	valuesStr := matches[3]
	valueMatches := enumValueRegex.FindAllStringSubmatch(valuesStr, -1)

	values := make([]string, 0, len(valueMatches))
	for _, match := range valueMatches {
		if len(match) > 1 {
			values = append(values, match[1])
		}
	}

	return types.SchemaEnum{
		Name:   enumName,
		Values: values,
	}, nil
}

func (sm *SchemaManager) parseCreateTableStatement(stmt string) (types.SchemaTable, error) {
	matches := tableRegex.FindStringSubmatch(stmt)
	if len(matches) < 2 {
		return types.SchemaTable{}, fmt.Errorf("could not extract table name from: %s", stmt)
	}

	tableName := stripNameQuotes(matches[1])
	if tableName == "" {
		return types.SchemaTable{}, fmt.Errorf("could not extract table name")
	}

	// Find the true closing paren of column definitions.
	// CQL has extra parens in: WITH CLUSTERING ORDER BY (col DESC)
	// and: PRIMARY KEY ((col1, col2), col3)
	// We track paren depth from the first ( after CREATE TABLE to find the matching ).
	start := strings.Index(stmt, "(")
	if start == -1 {
		return types.SchemaTable{}, fmt.Errorf("invalid CREATE TABLE syntax")
	}
	parenEnd := findMatchingParen(stmt, start)
	if parenEnd == -1 {
		return types.SchemaTable{}, fmt.Errorf("unclosed parenthesis in: %s", stmt)
	}

	columns, foreignKeys, err := sm.parseColumnDefinitionsAndConstraints(stmt[start+1 : parenEnd])
	if err != nil {
		return types.SchemaTable{}, err
	}

	sm.applyForeignKeys(columns, foreignKeys)

	// Extract raw PRIMARY KEY clause for CQL compound/composite keys
	compositePK := extractRawPKClause(stmt[start+1 : parenEnd])

	// Extract table suffix (e.g. "PARTITION BY RANGE (changed_at)")
	suffix := ""
	afterParen := strings.TrimSpace(stmt[parenEnd+1:])
	afterParen = strings.TrimSuffix(afterParen, ";")
	afterParen = strings.TrimSpace(afterParen)
	if afterParen != "" {
		suffix = afterParen
	}

	return types.SchemaTable{
		Name:        tableName,
		Columns:     columns,
		Indexes:     []types.SchemaIndex{},
		CompositePK: compositePK,
		Suffix:      suffix,
	}, nil
}

func findMatchingParen(s string, start int) int {
	if start >= len(s) || s[start] != '(' {
		return -1
	}
	depth := 0
	inString := false
	for i := start; i < len(s); i++ {
		if inString {
			if s[i] == '\'' {
				// Check for escaped quote ''
				if i+1 < len(s) && s[i+1] == '\'' {
					i++ // skip escaped quote
					continue
				}
				inString = false
			}
			continue
		}
		switch s[i] {
		case '\'':
			inString = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// extractRawPKClause returns the raw parenthesised content of a table-level PRIMARY KEY,
// e.g. "((country, city), order_id)" — used by ScyllaDB to reconstruct compound PKs.
// Returns "" if no table-level PRIMARY KEY is found (single-column inline PK).
func extractRawPKClause(columnDefs string) string {
	upper := strings.ToUpper(columnDefs)
	idx := strings.Index(upper, "PRIMARY KEY")
	if idx == -1 {
		return ""
	}
	rest := strings.TrimSpace(columnDefs[idx+len("PRIMARY KEY"):])
	if len(rest) == 0 || rest[0] != '(' {
		return ""
	}
	depth, end := 0, 0
	for i, ch := range rest {
		if ch == '(' {
			depth++
		} else if ch == ')' {
			depth--
			if depth == 0 {
				end = i + 1
				break
			}
		}
	}
	return rest[:end]
}

func (sm *SchemaManager) applyForeignKeys(columns []types.SchemaColumn, foreignKeys []foreignKeyConstraint) {
	for _, fk := range foreignKeys {
		for i := range columns {
			if columns[i].Name == fk.ColumnName {
				columns[i].ForeignKeyTable = fk.ReferencedTable
				columns[i].ForeignKeyColumn = fk.ReferencedColumn
				columns[i].OnDeleteAction = fk.OnDeleteAction
				columns[i].OnUpdateAction = fk.OnUpdateAction
				break
			}
		}
	}
}

func (sm *SchemaManager) parseColumnDefinitionsAndConstraints(columnDefs string) ([]types.SchemaColumn, []foreignKeyConstraint, error) {
	var columns []types.SchemaColumn
	var foreignKeys []foreignKeyConstraint

	for _, colDef := range sm.splitColumnDefinitions(columnDefs) {
		if colDef = strings.TrimSpace(colDef); colDef == "" {
			continue
		}

		if sm.isTableConstraint(colDef) {
			if fk := sm.parseForeignKeyConstraint(colDef); fk != nil {
				foreignKeys = append(foreignKeys, *fk)
			}
			// Mark PRIMARY KEY columns as primary and non-nullable
			if upper := strings.ToUpper(strings.TrimSpace(colDef)); strings.HasPrefix(upper, "PRIMARY KEY") {
				pkColNames := extractPKColumnNames(colDef)
				for i := range columns {
					for _, pkCol := range pkColNames {
						if strings.EqualFold(columns[i].Name, pkCol) {
							columns[i].IsPrimary = true
							columns[i].Nullable = false
						}
					}
				}
			}
			continue
		}

		column, err := sm.parseColumnDefinition(colDef)
		if err != nil {
			return nil, nil, err
		}
		columns = append(columns, column)
	}

	return columns, foreignKeys, nil
}

// extractPKColumnNames extracts all column names from a PRIMARY KEY clause,
// handling both simple PRIMARY KEY (col) and compound PRIMARY KEY ((pk1, pk2), ck1, ck2).
func extractPKColumnNames(constraint string) []string {
	// Find the outer paren content after "PRIMARY KEY"
	upper := strings.ToUpper(constraint)
	idx := strings.Index(upper, "PRIMARY KEY")
	if idx == -1 {
		return nil
	}
	rest := strings.TrimSpace(constraint[idx+len("PRIMARY KEY"):])
	if len(rest) == 0 || rest[0] != '(' {
		return nil
	}
	// Find matching close paren
	depth := 0
	end := 0
	for i, ch := range rest {
		if ch == '(' {
			depth++
		} else if ch == ')' {
			depth--
			if depth == 0 {
				end = i
				break
			}
		}
	}
	inner := rest[1:end] // strip outer parens
	// Remove nested parens (partition key group) and split by comma
	inner = strings.NewReplacer("(", "", ")", "").Replace(inner)
	var cols []string
	for part := range strings.SplitSeq(inner, ",") {
		col := strings.Trim(strings.TrimSpace(part), `"`)
		if col != "" {
			cols = append(cols, col)
		}
	}
	return cols
}

func (sm *SchemaManager) parseForeignKeyConstraint(constraint string) *foreignKeyConstraint {
	matches := parserFKRegex.FindStringSubmatch(constraint)
	if len(matches) >= 4 {
		fk := &foreignKeyConstraint{
			ColumnName:       matches[1],
			ReferencedTable:  matches[2],
			ReferencedColumn: matches[3],
		}
		if len(matches) >= 5 && matches[4] != "" {
			fk.OnDeleteAction = strings.ToUpper(matches[4])
		}
		if len(matches) >= 6 && matches[5] != "" {
			fk.OnUpdateAction = strings.ToUpper(matches[5])
		}
		return fk
	}
	return nil
}

func (sm *SchemaManager) splitColumnDefinitions(defs string) []string {
	var result []string
	var current strings.Builder
	parenLevel := 0
	angleLevel := 0
	inString := false

	for i := 0; i < len(defs); i++ {
		char := rune(defs[i])
		if inString {
			current.WriteByte(defs[i])
			if char == '\'' {
				// Handle escaped quote ''
				if i+1 < len(defs) && defs[i+1] == '\'' {
					i++
					current.WriteByte(defs[i])
				} else {
					inString = false
				}
			}
			continue
		}
		switch char {
		case '\'':
			inString = true
			current.WriteRune(char)
		case '(':
			parenLevel++
			current.WriteRune(char)
		case ')':
			parenLevel--
			current.WriteRune(char)
		case '<':
			// Track angle brackets only at depth 0 (CQL types: frozen<type>, map<k,v>).
			// Inside parens, < is a SQL comparison operator (e.g. age < 18).
			if parenLevel == 0 {
				angleLevel++
			}
			current.WriteRune(char)
		case '>':
			if angleLevel > 0 && parenLevel == 0 {
				angleLevel--
			}
			current.WriteRune(char)
		case ',':
			if parenLevel == 0 && angleLevel == 0 {
				result = append(result, current.String())
				current.Reset()
			} else {
				current.WriteRune(char)
			}
		default:
			current.WriteRune(char)
		}
	}

	if current.Len() > 0 {
		result = append(result, current.String())
	}
	return result
}

func (sm *SchemaManager) isTableConstraint(def string) bool {
	def = strings.ToUpper(strings.TrimSpace(def))
	prefixes := []string{"PRIMARY KEY", "FOREIGN KEY", "UNIQUE", "CHECK", "CONSTRAINT", "STATIC"}

	for _, prefix := range prefixes {
		if strings.HasPrefix(def, prefix) {
			return true
		}
	}
	return false
}

func (sm *SchemaManager) parseColumnDefinition(colDef string) (types.SchemaColumn, error) {
	colDef = strings.TrimSpace(colDef)

	// Extract column name (first token)
	spaceIdx := strings.IndexAny(colDef, " \t")
	if spaceIdx == -1 {
		return types.SchemaColumn{}, fmt.Errorf("invalid column definition: %s", colDef)
	}

	colName := strings.Trim(colDef[:spaceIdx], `"`)
	rest := strings.TrimSpace(colDef[spaceIdx+1:])

	if rest == "" {
		return types.SchemaColumn{}, fmt.Errorf("invalid column definition (no type): %s", colDef)
	}

	column := types.SchemaColumn{
		Name:     colName,
		Nullable: true,
	}

	// Extract type - handle parentheses for types like DECIMAL(10, 2)
	// Also handle CQL types: frozen<type>, set<type>, list<type>, map<k,v>, tuple<a,b,c>
	restUpper := strings.ToUpper(rest)

	// Handle multi-word types first
	if strings.HasPrefix(restUpper, "TIMESTAMP WITH TIME ZONE") {
		column.Type = "TIMESTAMP WITH TIME ZONE"
	} else if strings.HasPrefix(restUpper, "TIMESTAMP WITHOUT TIME ZONE") {
		column.Type = "TIMESTAMP WITHOUT TIME ZONE"
	} else if strings.HasPrefix(restUpper, "TIME WITH TIME ZONE") {
		column.Type = "TIME WITH TIME ZONE"
	} else if strings.HasPrefix(restUpper, "TIME WITHOUT TIME ZONE") {
		column.Type = "TIME WITHOUT TIME ZONE"
	} else if strings.HasPrefix(restUpper, "DOUBLE PRECISION") {
		column.Type = "DOUBLE PRECISION"
	} else if strings.HasPrefix(restUpper, "CHARACTER VARYING") {
		column.Type = "CHARACTER VARYING"
	} else {
		// Extract type: handles parens (), angle brackets <>, and simple types
		// CQL types: frozen<address>, set<text>, map<text,text>, list<frozen<x>>, tuple<text,int,double>
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
				// Only track angle brackets outside parens (CQL types).
				// Inside parens < is a SQL comparison operator.
				if parenDepth == 0 {
					angleDepth++
				}
			case '>':
				if parenDepth == 0 && angleDepth > 0 {
					angleDepth--
				}
			case ' ', '\t':
				if parenDepth <= 0 && angleDepth <= 0 {
					typeEnd = i
					goto typeDone
				}
			}
		}
	typeDone:
		if typeEnd == 0 {
			typeEnd = len(rest)
		}
		column.Type = strings.TrimSuffix(rest[:typeEnd], ",")
	}

	sm.parseColumnConstraints(&column, colDef)
	return column, nil
}

func (sm *SchemaManager) parseColumnConstraints(column *types.SchemaColumn, colDef string) {
	defUpper := strings.ToUpper(colDef)

	constraints := map[string]func(){
		"NOT NULL":       func() { column.Nullable = false },
		"PRIMARY KEY":    func() { column.IsPrimary = true },
		"UNIQUE":         func() { column.IsUnique = true },
		"AUTOINCREMENT":  func() { column.IsPrimary = true; column.IsAutoIncrement = true },
		"AUTO_INCREMENT": func() { column.IsPrimary = true; column.IsAutoIncrement = true },
		"SERIAL":         func() { column.IsPrimary = true; column.IsAutoIncrement = true },
		"BIGSERIAL":      func() { column.IsPrimary = true; column.IsAutoIncrement = true },
		"SMALLSERIAL":    func() { column.IsPrimary = true; column.IsAutoIncrement = true },
	}
	for constraint, action := range constraints {
		if strings.Contains(defUpper, constraint) {
			action()
		}
	}

	// GENERATED ALWAYS AS IDENTITY (PostgreSQL)
	if parserIdentityRegex.MatchString(colDef) {
		column.IsIdentity = true
		column.IsAutoIncrement = true
		column.IsPrimary = true
	}

	// GENERATED ALWAYS AS (expr) STORED|VIRTUAL
	genIdx := strings.Index(defUpper, "GENERATED")
	if genIdx != -1 {
		asIdx := strings.Index(defUpper[genIdx:], "AS")
		if asIdx != -1 {
			rest := colDef[genIdx+asIdx+2:] // skip "AS"
			rest = strings.TrimSpace(rest)
			if len(rest) > 0 && rest[0] == '(' {
				depth := 0
				end := 0
				inLiteral := false
				for end < len(rest) {
					if inLiteral {
						if rest[end] == '\'' {
							if end+1 < len(rest) && rest[end+1] == '\'' {
								end++ // skip escaped quote
							} else {
								inLiteral = false
							}
						}
					} else {
						switch rest[end] {
						case '\'':
							inLiteral = true
						case '(':
							depth++
						case ')':
							depth--
							if depth == 0 {
								end++
								goto genDone
							}
						}
					}
					end++
				}
			genDone:
				if depth == 0 && end > 1 {
					column.Generated = strings.TrimSpace(rest[1 : end-1])
					// Generated columns are nullable only if explicitly stated
					if !strings.Contains(defUpper, "NOT NULL") && !strings.Contains(defUpper, "PRIMARY KEY") {
						column.Nullable = true
					}
				}
			}
		}
	}

	if matches := parserReferencesRegex.FindStringSubmatch(colDef); len(matches) >= 3 {
		column.ForeignKeyTable = matches[1]
		column.ForeignKeyColumn = matches[2]
		if onDeleteMatches := parserOnDeleteRegex.FindStringSubmatch(colDef); len(onDeleteMatches) >= 2 {
			column.OnDeleteAction = strings.ToUpper(onDeleteMatches[1])
		}
		if onUpdateMatches := parserOnUpdateRegex.FindStringSubmatch(colDef); len(onUpdateMatches) >= 2 {
			column.OnUpdateAction = strings.ToUpper(onUpdateMatches[1])
		}
	}

	// CHECK constraint with balanced parentheses
	checkStart := -1
	if idx := strings.Index(strings.ToUpper(colDef), "CHECK("); idx != -1 {
		checkStart = idx
	} else if idx := strings.Index(strings.ToUpper(colDef), "CHECK ("); idx != -1 {
		checkStart = idx
	}
	if checkStart != -1 {
		parenIdx := strings.Index(colDef[checkStart:], "(")
		if parenIdx != -1 {
			start := checkStart + parenIdx + 1
			depth := 1
			end := start
			for end < len(colDef) && depth > 0 {
				if colDef[end] == '(' {
					depth++
				} else if colDef[end] == ')' {
					depth--
				}
				if depth > 0 {
					end++
				}
			}
			if depth == 0 {
				column.Check = strings.TrimSpace(colDef[start:end])
			}
		}
	}

	if def := extractDefault(colDef); def != "" {
		column.Default = def
	}
}

// extractDefault extracts the DEFAULT value from a column definition, properly
// handling nested parentheses (e.g. DEFAULT (unixepoch()), DEFAULT (gen_random_uuid())).
func extractDefault(colDef string) string {
	upper := strings.ToUpper(colDef)
	idx := strings.Index(upper, "DEFAULT ")
	if idx == -1 {
		// Also try DEFAULT immediately followed by ( or '
		idx = strings.Index(upper, "DEFAULT(")
		if idx == -1 {
			return ""
		}
		// Position after "DEFAULT"
		idx += len("DEFAULT")
	} else {
		// Position after "DEFAULT "
		idx += len("DEFAULT ")
	}

	rest := strings.TrimSpace(colDef[idx:])
	if len(rest) == 0 {
		return ""
	}

	switch rest[0] {
	case '\'':
		// Quoted string: find the matching closing quote (handle '' escapes)
		end := 1
		for end < len(rest) {
			if rest[end] == '\'' {
				if end+1 < len(rest) && rest[end+1] == '\'' {
					end += 2 // skip escaped quote
				} else {
					end++
					break
				}
			} else {
				end++
			}
		}
		return rest[:end]
	case '(':
		// Parenthesized expression: balance parentheses (handles nesting)
		depth := 0
		end := 0
		inStr := false
		for end < len(rest) {
			if inStr {
				if rest[end] == '\'' {
					if end+1 < len(rest) && rest[end+1] == '\'' {
						end++ // skip escaped quote
					} else {
						inStr = false
					}
				}
			} else {
				switch rest[end] {
				case '\'':
					inStr = true
				case '(':
					depth++
				case ')':
					depth--
					if depth == 0 {
						return rest[:end+1]
					}
				}
			}
			end++
		}
		// If unbalanced, return what we have
		return rest[:end]
	default:
		// Simple value (number, identifier, keyword): read until comma or whitespace
		end := 0
		for end < len(rest) && rest[end] != ',' && rest[end] != ' ' && rest[end] != '\t' && rest[end] != '\n' {
			end++
		}
		return rest[:end]
	}
}
