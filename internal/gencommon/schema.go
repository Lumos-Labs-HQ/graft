package gencommon

import (
	"regexp"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/parser"
	"github.com/Lumos-Labs-HQ/flash/internal/utils"
)

// SchemaExpander provides schema-based query column expansion and model type detection.
type SchemaExpander struct {
	Tables []*parser.Table
}

// NewSchemaExpander creates a SchemaExpander from a parsed schema.
func NewSchemaExpander(schema *parser.Schema) *SchemaExpander {
	if schema == nil {
		return &SchemaExpander{}
	}
	return &SchemaExpander{Tables: schema.Tables}
}

// ExpandWildcardColumns expands SELECT * columns. Resolves table aliases
// (e.g. f.*, u.*) and expands each wildcard to the corresponding table's columns.
// When column names conflict across tables, they are prefixed with the table alias.
func (e *SchemaExpander) ExpandWildcardColumns(query *parser.Query) []*parser.QueryColumn {
	if e == nil || len(e.Tables) == 0 || len(query.Columns) == 0 {
		return query.Columns
	}

	// Count wildcards
	wildcardCount := 0
	for _, col := range query.Columns {
		if col.Name == "*" {
			wildcardCount++
		}
	}
	if wildcardCount == 0 {
		return query.Columns
	}

	// Build alias → table map from SQL: "FROM tbl alias" and "JOIN tbl alias"
	aliasMap := extractTableAliases(query.SQL)

	// Also map table name → table for direct lookup
	tableByName := make(map[string]*parser.Table)
	for _, t := range e.Tables {
		name := t.Name
		if _, after, ok := strings.CutLast(name, "."); ok {
			name = after
		}
		tableByName[strings.ToLower(name)] = t
	}

	// Single wildcard — expand from primary table or resolved alias
	if wildcardCount == 1 {
		// Find the wildcard column
		var wcCol *parser.QueryColumn
		for _, col := range query.Columns {
			if col.Name == "*" {
				wcCol = col
				break
			}
		}
		// Resolve table: use alias map if qualifier present, else primary table
		resolvedTable := ""
		if wcCol.Table != "" {
			if resolved, ok := aliasMap[strings.ToLower(wcCol.Table)]; ok {
				resolvedTable = resolved
			} else {
				resolvedTable = wcCol.Table
			}
		} else {
			resolvedTable = utils.ExtractTableName(query.SQL)
		}
		if resolvedTable == "" {
			return query.Columns
		}
		if _, after, ok := strings.CutLast(resolvedTable, "."); ok {
			resolvedTable = after
		}
		t := tableByName[strings.ToLower(resolvedTable)]
		if t == nil {
			return query.Columns
		}
		expanded := make([]*parser.QueryColumn, 0, len(t.Columns)+len(query.Columns))
		for _, col := range query.Columns {
			if col.Name == "*" {
				for _, tc := range t.Columns {
					if tc.Name == "*" || strings.HasPrefix(tc.Name, "/*") {
						continue
					}
					expanded = append(expanded, &parser.QueryColumn{
						Name: tc.Name, Type: tc.Type, Table: t.Name, Nullable: tc.Nullable,
					})
				}
			} else {
				expanded = append(expanded, col)
			}
		}
		return expanded
	}

	// Multiple wildcards: expand each using its table qualifier
	// First pass: collect all expanded columns to detect name conflicts
	type expandedCol struct {
		alias string
		col   *parser.QueryColumn
	}
	var allCols []expandedCol

	for _, col := range query.Columns {
		if col.Name != "*" {
			allCols = append(allCols, expandedCol{"", col})
			continue
		}
		// Resolve the table from the qualifier (col.Table holds the alias like "f", "u")
		alias := col.Table
		tableName := alias
		if resolved, ok := aliasMap[strings.ToLower(alias)]; ok {
			tableName = resolved
		}
		t := tableByName[strings.ToLower(tableName)]
		if t == nil {
			// Can't resolve — skip this wildcard
			continue
		}
		for _, tc := range t.Columns {
			if tc.Name == "*" || strings.HasPrefix(tc.Name, "/*") {
				continue
			}
			allCols = append(allCols, expandedCol{alias, &parser.QueryColumn{
				Name: tc.Name, Type: tc.Type, Table: t.Name, Nullable: tc.Nullable,
			}})
		}
	}

	// Detect duplicate names
	nameCounts := make(map[string]int)
	for _, ec := range allCols {
		nameCounts[strings.ToLower(ec.col.Name)]++
	}

	// Build final list: prefix duplicates with alias
	expanded := make([]*parser.QueryColumn, 0, len(allCols))
	for _, ec := range allCols {
		col := ec.col
		if nameCounts[strings.ToLower(col.Name)] > 1 && ec.alias != "" {
			// Prefix with alias to disambiguate
			expanded = append(expanded, &parser.QueryColumn{
				Name:     ec.alias + "_" + col.Name,
				Type:     col.Type,
				Table:    col.Table,
				Nullable: true, // JOIN columns are potentially nullable
			})
		} else {
			expanded = append(expanded, col)
		}
	}
	return expanded
}

// extractTableAliases extracts alias → table_name mappings from FROM/JOIN clauses.
func extractTableAliases(sql string) map[string]string {
	m := make(map[string]string)
	// Match: FROM/JOIN table alias (alias is a word that's NOT a SQL keyword)
	re := regexp.MustCompile(`(?i)(?:FROM|JOIN)\s+(\w+)\s+(?:AS\s+)?(\w+)`)
	keywords := map[string]bool{
		"ON": true, "LEFT": true, "RIGHT": true, "INNER": true, "OUTER": true,
		"CROSS": true, "WHERE": true, "AND": true, "JOIN": true, "SET": true,
		"ORDER": true, "GROUP": true, "HAVING": true, "LIMIT": true, "AS": true,
		"NATURAL": true, "FULL": true, "SELECT": true, "INTO": true, "VALUES": true,
	}
	for _, match := range re.FindAllStringSubmatch(sql, -1) {
		alias := match[2]
		if keywords[strings.ToUpper(alias)] {
			continue
		}
		m[strings.ToLower(alias)] = match[1]
	}
	return m
}

// ModelTypeForQuery returns the model struct name if the query columns exactly
// match a table in the schema — enabling reuse of the model type instead of
// generating a duplicate row struct.
func (e *SchemaExpander) ModelTypeForQuery(query *parser.Query, columns []*parser.QueryColumn) string {
	if e == nil || len(columns) == 0 || len(e.Tables) == 0 {
		return ""
	}
	tableName := utils.ExtractTableName(query.SQL)
	if tableName == "" {
		return ""
	}
	// Strip keyspace prefix for lookup
	if _, after, ok := strings.CutLast(tableName, "."); ok {
		tableName = after
	}

	for _, t := range e.Tables {
		name := t.Name
		if _, after, ok := strings.CutLast(name, "."); ok {
			name = after
		}
		if !strings.EqualFold(name, tableName) || len(t.Columns) != len(columns) {
			continue
		}
		match := true
		for i, col := range columns {
			if !strings.EqualFold(col.Name, t.Columns[i].Name) {
				match = false
				break
			}
		}
		if match {
			return utils.ToPascalCase(tableName)
		}
	}
	return ""
}

// ModelTypeForQueryByName is the name-based variant of ModelTypeForQuery for
// ORMs that map result columns by NAME rather than position (sqlx FromRow /
// query_as!). The column SET must match the table — same names, types, and
// nullability — but the SELECT order may differ from the schema order.
func (e *SchemaExpander) ModelTypeForQueryByName(query *parser.Query, columns []*parser.QueryColumn) string {
	if e == nil || len(columns) == 0 || len(e.Tables) == 0 {
		return ""
	}
	tableName := utils.ExtractTableName(query.SQL)
	if tableName == "" {
		return ""
	}
	// Strip keyspace prefix for lookup
	if _, after, ok := strings.CutLast(tableName, "."); ok {
		tableName = after
	}

	for _, t := range e.Tables {
		name := t.Name
		if _, after, ok := strings.CutLast(name, "."); ok {
			name = after
		}
		if !strings.EqualFold(name, tableName) || len(t.Columns) != len(columns) {
			continue
		}
		byName := make(map[string]*parser.Column, len(t.Columns))
		for _, tc := range t.Columns {
			byName[strings.ToLower(tc.Name)] = tc
		}
		match := true
		for _, col := range columns {
			tc, ok := byName[strings.ToLower(col.Name)]
			if !ok || !strings.EqualFold(tc.Type, col.Type) || tc.Nullable != col.Nullable {
				match = false
				break
			}
		}
		if match {
			return utils.ToPascalCase(tableName)
		}
	}
	return ""
}
