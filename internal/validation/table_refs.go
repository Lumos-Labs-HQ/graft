package validation

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/utils"
)

// ValidateTableReferences checks if tables referenced in queries exist in the schema
func ValidateTableReferences(sql string, schema any, sourceFile string) error {
	if schema == nil {
		return nil
	}

	if sourceFile == "" {
		sourceFile = "queries"
	}

	tableNames := extractTableNamesFromSchema(schema)
	if tableNames == nil {
		return nil
	}

	cteNames := make(map[string]bool, 4)
	cteMatches := ctePatternRegex.FindAllStringSubmatch(sql, -1)
	for _, match := range cteMatches {
		if len(match) > 1 {
			cteName := match[1]
			if !utils.IsSQLKeyword(cteName) {
				cteNames[strings.ToLower(cteName)] = true
			}
		}
	}

	// Detect subquery aliases: words immediately following ")" in FROM/JOIN context
	subqueryAliasRegex := regexp.MustCompile(`\)\s+(?:AS\s+)?(\w+)`)
	for _, match := range subqueryAliasRegex.FindAllStringSubmatch(sql, -1) {
		if len(match) > 1 {
			alias := match[1]
			if !utils.IsSQLKeyword(alias) {
				cteNames[strings.ToLower(alias)] = true
			}
		}
	}

	matches := tablePatternRegex.FindAllStringSubmatch(utils.StripParenthesizedContent(sql), -1)

	foundTableRefs := false

	for _, match := range matches {
		if len(match) < 2 {
			continue
		}

		tableName := match[1]
		if isDynamicOrVirtualTable(tableName) {
			continue
		}

		if utils.IsSQLKeyword(tableName) {
			continue
		}

		if cteNames[strings.ToLower(tableName)] {
			continue
		}

		foundTableRefs = true

		tableExists := tableNames[strings.ToLower(tableName)]

		// Strip keyspace prefix and retry (e.g. "myapp.users" → "users")
		if !tableExists && strings.Contains(tableName, ".") {
			dotIdx := strings.LastIndex(tableName, ".")
			stripped := strings.ToLower(tableName[dotIdx+1:])
			tableExists = tableNames[stripped]
		}

		// Reverse direction: the SCHEMA table is keyspace-qualified
		// (e.g. "myks.users") but the query uses the plain name ("users").
		// Mirrors parser.matchesTableName so validation cannot contradict
		// the query analyzer's own table resolution.
		if !tableExists {
			plain := strings.ToLower(tableName)
			for name := range tableNames {
				if _, after, ok := strings.CutLast(name, "."); ok && after == plain {
					tableExists = true
					break
				}
			}
		}

		if !tableExists {
			lines := strings.Split(sql, "\n")
			lineNum := 1
			colPos := 1
			upperTable := strings.ToUpper(tableName)

			for i, line := range lines {
				upperLine := strings.ToUpper(line)
				if strings.Contains(upperLine, upperTable) {
					lineNum = i + 1
					colPos = strings.Index(upperLine, upperTable) + 1
					break
				}
			}

			return fmt.Errorf("# package flash\ndb\\queries\\%s.sql:%d:%d: relation \"%s\" does not exist", sourceFile, lineNum, colPos, tableName)
		}
	}

	if foundTableRefs && len(tableNames) == 0 {
		return fmt.Errorf("# package flash\ndb\\queries\\%s.sql:1:1: no tables found in schema, but query references tables", sourceFile)
	}

	return nil
}

// SQLite exposes table-valued JSON functions through FROM/JOIN syntax. They
// are valid virtual row sources, not schema tables. Dynamic placeholders are
// likewise resolved by the application at runtime.
func isDynamicOrVirtualTable(name string) bool {
	trimmed := strings.Trim(strings.TrimSpace(name), "\"'`")
	if strings.ContainsAny(trimmed, "{}") {
		return true
	}
	base := strings.ToLower(trimmed)
	if dot := strings.LastIndexByte(base, '('); dot >= 0 {
		base = base[:dot]
	}
	switch base {
	case "json_each", "json_tree", "json_each_text", "json_tree_text":
		return true
	default:
		return false
	}
}
