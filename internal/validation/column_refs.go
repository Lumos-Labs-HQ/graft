package validation

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/utils"
)

// ValidateColumnReferences checks if columns referenced in queries exist in the schema
func ValidateColumnReferences(sql string, schema any, sourceFile string) error {
	if schema == nil {
		return nil
	}

	if sourceFile == "" {
		sourceFile = "queries"
	}

	sqlUpper := strings.ToUpper(sql)
	if strings.Contains(sqlUpper, "UNION") {
		return nil
	}

	// Pre-process SQL: strip subqueries and OVER(...) blocks
	outerSQL := stripSubqueryBlocks(stripOverClauses(sql))

	schemaVal := reflect.ValueOf(schema)
	if schemaVal.Kind() == reflect.Pointer {
		schemaVal = schemaVal.Elem()
	}

	if schemaVal.Kind() != reflect.Struct {
		return nil
	}

	tablesField := schemaVal.FieldByName("Tables")
	if !tablesField.IsValid() || tablesField.Kind() != reflect.Slice {
		return nil
	}

	type tableInfo struct {
		name    string
		columns map[string]bool
	}

	tables := make(map[string]*tableInfo)
	for i := 0; i < tablesField.Len(); i++ {
		tablePtr := tablesField.Index(i)
		if tablePtr.Kind() == reflect.Pointer {
			tablePtr = tablePtr.Elem()
		}
		if tablePtr.Kind() == reflect.Struct {
			nameField := tablePtr.FieldByName("Name")
			columnsField := tablePtr.FieldByName("Columns")

			if nameField.IsValid() && nameField.Kind() == reflect.String {
				key := strings.ToLower(nameField.String())
				tblInfo := &tableInfo{
					name:    nameField.String(),
					columns: make(map[string]bool),
				}

				tables[key] = tblInfo
				if _, after, ok := strings.CutLast(key, "."); ok {
					tables[after] = tblInfo
				}

				if columnsField.IsValid() && columnsField.Kind() == reflect.Slice {
					for j := 0; j < columnsField.Len(); j++ {
						colPtr := columnsField.Index(j)
						if colPtr.Kind() == reflect.Pointer {
							colPtr = colPtr.Elem()
						}
						if colPtr.Kind() == reflect.Struct {
							colNameField := colPtr.FieldByName("Name")
							if colNameField.IsValid() && colNameField.Kind() == reflect.String {
								tblInfo.columns[strings.ToLower(colNameField.String())] = true
							}
						}
					}
				}
			}
		}
	}

	aliasToTable := make(map[string]string, 4)

	matches := tableAliasPatternRegex.FindAllStringSubmatch(sql, -1)
	for _, match := range matches {
		if len(match) >= 3 {
			aliasToTable[strings.ToLower(match[2])] = strings.ToLower(match[1])
		}
	}

	matches = joinPatternRegex.FindAllStringSubmatch(sql, -1)
	for _, match := range matches {
		if len(match) >= 3 {
			aliasToTable[strings.ToLower(match[2])] = strings.ToLower(match[1])
		}
	}

	columnRefs := columnRefPatternRegex.FindAllStringSubmatch(sql, -1)

	for _, ref := range columnRefs {
		if len(ref) < 3 {
			continue
		}

		tableOrAlias := ref[1]
		columnName := ref[2]

		if utils.IsSQLKeyword(tableOrAlias) || utils.IsSQLKeyword(columnName) {
			continue
		}

		tableName := strings.ToLower(tableOrAlias)
		if realTable, ok := aliasToTable[tableName]; ok {
			tableName = realTable
		}

		table, tableExists := tables[tableName]
		if !tableExists {
			continue
		}

		if !table.columns[strings.ToLower(columnName)] {
			lines := strings.Split(sql, "\n")
			lineNum := 0
			colPos := 0
			for i, line := range lines {
				if strings.Contains(line, ref[0]) {
					lineNum = i + 1
					colPos = strings.Index(line, ref[0]) + len(tableOrAlias) + 1
					break
				}
			}

			return fmt.Errorf("# package flash\ndb\\queries\\%s.sql:%d:%d: column reference \"%s\" not found in table \"%s\"", sourceFile, lineNum, colPos, columnName, table.name)
		}
	}

	knownAliases := make(map[string]bool)
	for alias := range aliasToTable {
		knownAliases[alias] = true
	}

	aliasMatches := aliasExtractPatternRegex.FindAllStringSubmatch(sql, -1)
	for _, match := range aliasMatches {
		if len(match) >= 3 && match[2] != "" {
			knownAliases[strings.ToLower(match[2])] = true
		}
	}

	selectAliasMatches := selectAliasRegex.FindAllStringSubmatch(sql, -1)
	for _, match := range selectAliasMatches {
		if len(match) >= 2 {
			alias := strings.ToLower(match[1])
			if !utils.IsSQLKeyword(alias) {
				knownAliases[alias] = true
			}
		}
	}

	var primaryTable *tableInfo
	if fromMatch := fromPatternRegex.FindStringSubmatch(outerSQL); len(fromMatch) > 1 {
		primaryTable = tables[strings.ToLower(fromMatch[1])]
	}

	if primaryTable == nil {
		if insertMatch := insertPatternRegex.FindStringSubmatch(outerSQL); len(insertMatch) > 1 {
			primaryTable = tables[strings.ToLower(insertMatch[1])]
		}
	}

	hasJoin := joinCheckRegex.MatchString(outerSQL)

	if primaryTable != nil && !hasJoin {
		clausePatterns := []*regexp.Regexp{
			whereClauseRegex,
			setClauseRegex,
			orderByClauseRegex,
			groupByClauseRegex,
			havingClauseRegex,
		}

		for _, pattern := range clausePatterns {
			if matches := pattern.FindStringSubmatch(outerSQL); len(matches) > 1 {
				clauseText := stripStringLiterals(matches[1])
				colMatches := unqualifiedColPatternRegex.FindAllString(clauseText, -1)

				for _, colName := range colMatches {
					colLower := strings.ToLower(colName)

					if utils.IsSQLKeyword(colName) ||
						colLower == "true" || colLower == "false" || colLower == "null" ||
						colLower == "and" || colLower == "or" || colLower == "not" ||
						strings.Contains(clauseText, colName+"(") {
						continue
					}

					if paramCheckRegex.MatchString(colName) {
						continue
					}

					if knownAliases[colLower] {
						continue
					}

					// A bare table name is not a column reference — skip it.
					if tables[colLower] != nil {
						continue
					}

					if !primaryTable.columns[colLower] {
						lines := strings.Split(sql, "\n")
						lineNum := 1
						colPos := 1
						upperCol := strings.ToUpper(colName)

						for i, line := range lines {
							upperLine := strings.ToUpper(line)
							if strings.Contains(upperLine, upperCol) {
								lineNum = i + 1
								colPos = strings.Index(upperLine, upperCol) + 1
								break
							}
						}

						return fmt.Errorf("# package flash\ndb\\queries\\%s.sql:%d:%d: column \"%s\" does not exist in table \"%s\"",
							sourceFile, lineNum, colPos, colName, primaryTable.name)
					}
				}
			}
		}
	}

	return nil
}
