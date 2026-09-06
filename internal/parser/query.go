package parser

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/Lumos-Labs-HQ/flash/internal/config"
	"github.com/Lumos-Labs-HQ/flash/internal/utils"
	"github.com/Lumos-Labs-HQ/flash/internal/validation"
)

type QueryParser struct {
	Config       *config.Config
	insertRegex  *regexp.Regexp
	updateRegex  *regexp.Regexp
	deleteRegex  *regexp.Regexp
	typeInferrer *TypeInferrer
}

func NewQueryParser(cfg *config.Config) *QueryParser {
	return &QueryParser{
		Config:       cfg,
		insertRegex:  regexp.MustCompile(`(?i)INSERT\s+INTO\s+([^\s;]+)`),
		updateRegex:  regexp.MustCompile(`(?i)UPDATE\s+([^\s;]+)`),
		deleteRegex:  regexp.MustCompile(`(?i)DELETE\s+FROM\s+([^\s;]+)`),
		typeInferrer: NewTypeInferrer(),
	}
}

func (p *QueryParser) Parse(schema *Schema) ([]*Query, error) {
	// Inject schema into inferrer for cross-table type resolution
	p.typeInferrer = NewTypeInferrerWithSchema(schema)

	queriesPath := p.Config.Queries
	if !filepath.IsAbs(queriesPath) {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to get current working directory: %w", err)
		}
		queriesPath = filepath.Join(cwd, queriesPath)
	}

	files, _ := filepath.Glob(filepath.Join(queriesPath, "*.sql"))
	cqlFiles, _ := filepath.Glob(filepath.Join(queriesPath, "*.cql"))
	files = append(files, cqlFiles...)

	if len(files) == 0 {
		return []*Query{}, nil
	}

	// Use concurrent processing for better performance on large projects
	return p.parseFilesConcurrently(files, schema)
}

// parseFilesConcurrently processes query files in parallel using worker pool
func (p *QueryParser) parseFilesConcurrently(files []string, schema *Schema) ([]*Query, error) {
	// Create indexed schema for O(1) lookups
	indexedSchema := NewIndexedSchema(schema)

	// Determine optimal worker count (don't exceed CPU count or file count)
	numWorkers := min(runtime.NumCPU(), len(files))
	if numWorkers < 1 {
		numWorkers = 1
	}

	// Channels for work distribution and result collection
	type parseResult struct {
		queries []*Query
		err     error
		file    string
	}

	fileChan := make(chan string, len(files))
	resultChan := make(chan parseResult, len(files))

	// Launch worker goroutines
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Go(func() {
			for file := range fileChan {
				queries, err := p.parseQueryFile(file, indexedSchema.Schema)
				resultChan <- parseResult{
					queries: queries,
					err:     err,
					file:    file,
				}
			}
		})
	}

	// Send files to workers
	for _, file := range files {
		fileChan <- file
	}
	close(fileChan)

	// Wait for all workers to finish
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results
	allQueries := make([]*Query, 0, len(files)*4)
	for result := range resultChan {
		if result.err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", result.file, result.err)
		}
		allQueries = append(allQueries, result.queries...)
	}

	return allQueries, nil
}

func (p *QueryParser) parseQueryFile(filename string, schema *Schema) ([]*Query, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	baseName := filepath.Base(filename)
	sourceFileName := strings.TrimSuffix(baseName, filepath.Ext(baseName))

	queries := []*Query{}
	scanner := bufio.NewScanner(file)

	var currentQuery *Query
	var sqlLines []string
	var comment string
	var pendingRequired []string
	var pendingJsonTypes []*JsonType
	var pendingCache *CacheDef

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "-- name:") || strings.HasPrefix(line, "-- name :") {
			if currentQuery != nil {
				currentQuery.SQL = strings.TrimSpace(strings.Join(sqlLines, " "))
				currentQuery.Comment = comment
				currentQuery.SourceFile = sourceFileName
				if err := p.analyzeQuery(currentQuery, schema); err != nil {
					return nil, err
				}
				// Attach JSON types to matching columns
				attachJsonTypesToQuery(currentQuery)
				queries = append(queries, currentQuery)
			}

			_, after, ok := strings.Cut(line, "name")
			if !ok {
				continue
			}
			remainder := after
			remainder = strings.TrimLeft(remainder, " :")

			parts := strings.Fields(remainder)
			if len(parts) >= 2 {
				currentQuery = &Query{
					Name:         parts[0],
					Cmd:          parts[1],
					RequiredCols: pendingRequired,
					JsonTypes:    pendingJsonTypes,
					CacheDef:     pendingCache,
				}
				sqlLines = []string{}
				comment = ""
				pendingRequired = nil
				pendingJsonTypes = nil
				pendingCache = nil
			}
		} else if after, ok := strings.CutPrefix(line, "-- @required:"); ok {
			val := after
			val = strings.TrimSpace(val)
			var cols []string
			if val == "*" {
				cols = []string{"*"}
			} else {
				for col := range strings.SplitSeq(val, ",") {
					col = strings.TrimSpace(col)
					if col != "" {
						cols = append(cols, col)
					}
				}
			}
			// If we already have a currentQuery (annotation after -- name:), assign directly
			if currentQuery != nil && len(sqlLines) == 0 {
				currentQuery.RequiredCols = cols
			} else {
				// Before -- name: line, save as pending
				pendingRequired = cols
			}
		} else if strings.HasPrefix(line, "-- @json") {
			// Parse @json annotation
			jsonBasePath := ""
			if p.Config != nil {
				jsonBasePath = p.Config.JsonPath
			}
			jt, err := ParseJsonAnnotation(line, jsonBasePath)
			if err != nil {
				return nil, fmt.Errorf("in file %s: %w", filename, err)
			}
			if currentQuery != nil && len(sqlLines) == 0 {
				currentQuery.JsonTypes = append(currentQuery.JsonTypes, jt)
			} else {
				pendingJsonTypes = append(pendingJsonTypes, jt)
			}
		} else if strings.HasPrefix(line, "-- @cache") {
			// Parse @cache annotation: -- @cache {"ttl": "30s", "name": "UserCache", "tags": [...], "dep": [...]}
			cd, err := parseCacheAnnotation(line)
			if err != nil {
				return nil, fmt.Errorf("in file %s: %w", filename, err)
			}
			if currentQuery != nil && len(sqlLines) == 0 {
				currentQuery.CacheDef = cd
			} else {
				pendingCache = cd
			}
		} else if after, ok := strings.CutPrefix(line, "--"); ok {
			comment = after
			comment = strings.TrimSpace(comment)
		} else if currentQuery != nil {
			sqlLines = append(sqlLines, line)
		}
	}

	if currentQuery != nil {
		currentQuery.SQL = strings.TrimSpace(strings.Join(sqlLines, " "))
		currentQuery.Comment = comment
		currentQuery.SourceFile = sourceFileName
		if err := p.analyzeQuery(currentQuery, schema); err != nil {
			return nil, err
		}
		attachJsonTypesToQuery(currentQuery)
		queries = append(queries, currentQuery)
	}

	return queries, scanner.Err()
}

func (p *QueryParser) analyzeQuery(query *Query, schema *Schema) error {
	// Rewrite col IN ($1, $2, ...) → col = ANY($1) with a single array param,
	// renumbering all subsequent params. Only for PostgreSQL-style $N params.
	query.SQL = rewriteINListToANY(query.SQL)

	var tableName string
	// Strip parenthesized content to avoid matching function-internal FROM (e.g. EXTRACT(EPOCH FROM ...))
	cleaned := utils.StripParenthesizedContent(query.SQL)
	if match := fromRegex.FindStringSubmatch(cleaned); len(match) > 1 {
		tableName = stripIdentQuotes(match[1])
	}

	if tableName == "" {
		if match := p.insertRegex.FindStringSubmatch(query.SQL); len(match) > 1 {
			tableName = stripIdentQuotes(match[1])
		}
	}
	if tableName == "" {
		if match := p.updateRegex.FindStringSubmatch(query.SQL); len(match) > 1 {
			tableName = stripIdentQuotes(match[1])
		}
	}
	// Runtime-selected table placeholders and SQLite virtual table functions
	// are resolved by the database/application, not by the schema catalog.
	if isDynamicOrVirtualTableName(tableName) {
		tableName = ""
	}

	// Build CTE name set — CTEs are query-local, not tables in schema
	cteNames := make(map[string]bool)
	for _, m := range cteNameRegex.FindAllStringSubmatch(query.SQL, -1) {
		if len(m) > 1 {
			cteNames[strings.ToLower(m[1])] = true
		}
	}

	// Detect subquery aliases: words immediately following ")" in the original SQL
	// e.g. FROM (SELECT ...) ranked → "ranked" is a subquery alias, not a table
	subqAliasRe := regexp.MustCompile(`\)\s+(?:AS\s+)?(\w+)`)
	for _, m := range subqAliasRe.FindAllStringSubmatch(query.SQL, -1) {
		if len(m) > 1 && !utils.IsSQLKeyword(m[1]) {
			cteNames[strings.ToLower(m[1])] = true
		}
	}

	// If the extracted table name is actually a CTE, skip schema validation
	if cteNames[strings.ToLower(tableName)] {
		tableName = ""
	}

	// Normalize keyspace-qualified names: "ks"."tbl" → ks.tbl, ks.tbl → ks.tbl
	// Match against schema table names which may be keyspace-qualified or plain.
	var table *Table
	for _, t := range schema.Tables {
		if matchesTableName(t.Name, tableName) {
			table = t
			break
		}
	}

	// Fallback: strip keyspace prefix and retry.
	// e.g. query says "myapp.users" but schema has "users" (ScyllaDB single-keyspace mode)
	if table == nil && tableName != "" {
		if _, stripped, ok := strings.CutLast(tableName, "."); ok {
			for _, t := range schema.Tables {
				if strings.EqualFold(t.Name, stripped) {
					table = t
					break
				}
			}
		}
	}

	// Return an error when a referenced table is missing from the schema.
	if tableName != "" && table == nil {
		availableTables := make([]string, len(schema.Tables))
		for i, t := range schema.Tables {
			availableTables[i] = t.Name
		}
		return fmt.Errorf("table '%s' referenced in query '%s' does not exist in schema. Available tables: %v",
			tableName, query.Name, availableTables)
	}

	paramMatches := paramRegex.FindAllString(query.SQL, -1)

	var paramCount int
	if len(paramMatches) > 0 && paramMatches[0] == "?" {
		paramCount = len(paramMatches)
	} else {
		seen := make(map[string]bool, len(paramMatches))
		for _, p := range paramMatches {
			if !seen[p] {
				seen[p] = true
				paramCount++
			}
		}
	}

	query.Params = make([]*Param, paramCount)
	usedParamNames := make(map[string]int)
	// Extract ordered actual param numbers from the SQL so we map
	orderedParamNums := extractOrderedParamNums(query.SQL)

	// Validate INSERT/UPDATE columns exist in the schema.
	if table != nil {
		sqlUpper := strings.ToUpper(query.SQL)
		if strings.Contains(sqlUpper, "INSERT INTO") {
			if err := p.validateInsertColumns(query.SQL, table); err != nil {
				return fmt.Errorf("validation error in query '%s': %w", query.Name, err)
			}
		} else if strings.Contains(sqlUpper, "UPDATE") {
			if err := p.validateUpdateColumns(query.SQL, table); err != nil {
				return fmt.Errorf("validation error in query '%s': %w", query.Name, err)
			}
		}
	}

	for i := 0; i < paramCount; i++ {
		// Use the actual $N number from the SQL if available,
		// falling back to i+1 for ?-style parameters.
		paramNum := i + 1
		if i < len(orderedParamNums) && orderedParamNums[i] > 0 {
			paramNum = orderedParamNums[i]
		}
		paramName := fmt.Sprintf("param%d", i+1)
		var paramType string

		// Infer param name from SQL regardless of table availability
		inferredName := p.typeInferrer.InferParamName(query.SQL, paramNum)
		if inferredName != "" && inferredName != paramName {
			paramName = inferredName
		}

		if table != nil {
			paramType = p.typeInferrer.InferParamType(query.SQL, paramNum, table, paramName)
		} else {
			// Even without a table, infer from well-known param name patterns
			paramType = p.typeInferrer.InferParamTypeByName(paramName)
		}

		if count, exists := usedParamNames[paramName]; exists {
			usedParamNames[paramName] = count + 1
			paramName = fmt.Sprintf("%s%d", paramName, count+1)
		} else {
			usedParamNames[paramName] = 1
		}

		query.Params[i] = &Param{
			Name:     paramName,
			Type:     paramType,
			ParamNum: paramNum,
			Nullable: p.isCQLProvider() && p.isParamNullable(paramName, table),
		}
	}

	// Renumber $N placeholders to sequential $1, $2, ... so generated
	// when $1 is absent from the query.
	if len(orderedParamNums) > 0 {
		query.SQL = renumberParams(query.SQL, orderedParamNums)
	}

	sqlUpper := strings.ToUpper(query.SQL)
	sqlTrimmed := strings.TrimSpace(sqlUpper)

	isSelectQuery := strings.HasPrefix(sqlTrimmed, "SELECT") ||
		strings.HasPrefix(sqlTrimmed, "WITH") ||
		(strings.HasPrefix(sqlTrimmed, "(") && strings.Contains(sqlTrimmed, "SELECT"))
	isNotModifying := !utils.ContainsSQLKeyword(sqlTrimmed, "DELETE") &&
		!utils.ContainsSQLKeyword(sqlTrimmed, "UPDATE") &&
		!utils.ContainsSQLKeyword(sqlTrimmed, "INSERT")

	hasReturning := utils.ContainsSQLKeyword(sqlTrimmed, "RETURNING")

	if (isSelectQuery && isNotModifying) || hasReturning {
		var columnsStr string

		if hasReturning {
			if matches := returningRegex.FindStringSubmatch(query.SQL); len(matches) > 1 {
				columnsStr = strings.TrimSpace(matches[1])
			}
		} else {
			columnsStr = utils.ExtractSelectColumns(query.SQL)
		}

		if columnsStr != "" && strings.TrimSpace(columnsStr) != "*" {
			colNames := utils.SmartSplitColumns(columnsStr)

			if len(colNames) > 0 {
				query.Columns = make([]*QueryColumn, 0, len(colNames))

				for _, colName := range colNames {
					colName = strings.TrimSpace(colName)
					if colName == "" {
						continue
					}

					originalExpr := colName
					aliasName := ""

					allMatches := asRegex.FindAllStringIndex(colName, -1)
					if len(allMatches) > 0 {
						validMatch := -1
						colNameUpper := strings.ToUpper(colName)

						for i, allMatche := range slices.Backward(allMatches) {
							asPos := allMatche[0]
							parenDepth := 0
							caseDepth := 0

							for j := range asPos {
								switch colName[j] {
								case '(':
									parenDepth++
								case ')':
									parenDepth--
								}

								// Track CASE/END blocks
								if j+4 <= len(colNameUpper) && colNameUpper[j:j+4] == "CASE" {
									if j == 0 || !((colName[j-1] >= 'A' && colName[j-1] <= 'Z') || (colName[j-1] >= 'a' && colName[j-1] <= 'z')) {
										caseDepth++
									}
								}
								if j+3 <= len(colNameUpper) && colNameUpper[j:j+3] == "END" {
									if (j == 0 || !((colName[j-1] >= 'A' && colName[j-1] <= 'Z') || (colName[j-1] >= 'a' && colName[j-1] <= 'z'))) &&
										(j+3 >= len(colName) || !((colName[j+3] >= 'A' && colName[j+3] <= 'Z') || (colName[j+3] >= 'a' && colName[j+3] <= 'z'))) {
										caseDepth--
									}
								}
							}

							// If we're at depth 0 for both parentheses and CASE blocks, this AS is at the top level (column alias)
							if parenDepth == 0 && caseDepth == 0 {
								validMatch = i
								break
							}
						}

						if validMatch >= 0 {
							loc := allMatches[validMatch]
							originalExpr = strings.TrimSpace(colName[:loc[0]])
							aliasName = strings.TrimSpace(colName[loc[1]:])
							colName = aliasName
						}
					} else {
						if !strings.Contains(colName, "(") {
							if idx := strings.Index(colName, "."); idx != -1 {
								originalExpr = colName
								qualifier := strings.TrimSpace(colName[:idx])
								// Strip DISTINCT/ALL keywords from qualifier
								qualifier = regexp.MustCompile(`(?i)^(DISTINCT|ALL)\s+`).ReplaceAllString(qualifier, "")
								remainder := colName[idx+1:]
								// Preserve the table qualifier for wildcard columns (f.*, u.*)
								if remainder == "*" {
									query.Columns = append(query.Columns, &QueryColumn{
										Name:  "*",
										Type:  "string",
										Table: qualifier,
									})
									continue
								}
								colName = remainder
							}
						}
					}

					colType, nullable := p.inferColumnType(colName, originalExpr, query.SQL, schema, table)

					// isComputed: true if the original expression involves anything
					// beyond a simple column reference (function calls, operators, etc.)
					bareRefRe := regexp.MustCompile(`^\w+(\.\w+)?$`)
					isComputed := originalExpr != "" && !bareRefRe.MatchString(originalExpr)

					query.Columns = append(query.Columns, &QueryColumn{
						Name:         colName,
						Type:         colType,
						Table:        tableName,
						Nullable:     nullable,
						IsComputed:   isComputed,
						OriginalExpr: originalExpr,
					})
				}
			}
		}

		if len(query.Columns) == 0 {
			query.Columns = []*QueryColumn{{
				Name:  "*",
				Type:  "string",
				Table: tableName,
			}}
		}
	}

	hasJoin := strings.Contains(sqlUpper, "JOIN")
	hasUnion := strings.Contains(sqlUpper, "UNION")
	hasAggregate := strings.Contains(sqlUpper, "GROUP BY") ||
		strings.Contains(sqlUpper, "COUNT(") ||
		strings.Contains(sqlUpper, "SUM(") ||
		strings.Contains(sqlUpper, "AVG(") ||
		strings.Contains(sqlUpper, "MAX(") ||
		strings.Contains(sqlUpper, "MIN(") ||
		strings.Contains(sqlUpper, " FILTER ") ||
		strings.Contains(sqlUpper, "OVER(") ||
		strings.Contains(sqlUpper, " OVER ")

	// Table references are always validated — catches real typos.
	// Column *reference* validation (qualified refs + WHERE clause) runs for all queries.
	// Only the return-column existence check (alias vs schema) is skipped for complex queries
	// because aggregates/window functions/CTEs produce computed aliases not in any table.
	if err := validation.ValidateTableReferences(query.SQL, schema, query.SourceFile); err != nil {
		return err
	}
	if err := validation.ValidateColumnReferences(query.SQL, schema, query.SourceFile); err != nil {
		return err
	}

	if table != nil && len(query.Columns) > 0 && !hasJoin && !hasUnion && !hasAggregate {
		for _, queryCol := range query.Columns {
			if queryCol.Name == "*" {
				continue
			}

			if strings.Contains(queryCol.Name, "(") || strings.Contains(queryCol.Name, ")") {
				continue
			}

			// Skip column existence check when alias differs from the original
			// expression — e.g. "id AS post_id", "preferences->'key' AS pref_value"
			if queryCol.IsComputed || (queryCol.OriginalExpr != "" && !strings.EqualFold(queryCol.Name, queryCol.OriginalExpr) &&
				!strings.HasSuffix(strings.ToLower(queryCol.OriginalExpr), "."+strings.ToLower(queryCol.Name))) {
				continue
			}

			columnExists := false
			for _, schemaCol := range table.Columns {
				if strings.EqualFold(schemaCol.Name, queryCol.Name) {
					columnExists = true
					break
				}
			}

			if !columnExists {
				lines := strings.Split(query.SQL, "\n")
				lineNum := 1
				colPos := 1
				upperCol := strings.ToUpper(queryCol.Name)

				for i, line := range lines {
					upperLine := strings.ToUpper(line)
					if strings.Contains(upperLine, upperCol) {
						lineNum = i + 1
						colPos = strings.Index(upperLine, upperCol) + 1
						break
					}
				}

				sourceFile := query.SourceFile
				if sourceFile == "" {
					sourceFile = "queries"
				}
				return fmt.Errorf("# package FlashORM\ndb\\queries\\%s.sql:%d:%d: column \"%s\" does not exist in table \"%s\"",
					sourceFile, lineNum, colPos, queryCol.Name, table.Name)
			}
		}
	}

	// Apply @required annotation (CQL only): mark specified params as non-nullable
	// In CQL, all non-PK columns are nullable by default. @required lets users
	// declare which params must be provided (non-null in generated Params class).
	if len(query.RequiredCols) > 0 {
		if len(query.RequiredCols) == 1 && query.RequiredCols[0] == "*" {
			for _, param := range query.Params {
				param.Nullable = false
			}
		} else {
			paramMap := make(map[string]*Param)
			for _, param := range query.Params {
				paramMap[strings.ToLower(param.Name)] = param
			}
			for _, reqCol := range query.RequiredCols {
				if param, ok := paramMap[strings.ToLower(reqCol)]; ok {
					param.Nullable = false
				} else {
					return fmt.Errorf("@required param %q not found in query %q params. Available: %v",
						reqCol, query.Name, func() []string {
							names := make([]string, len(query.Params))
							for i, p := range query.Params {
								names[i] = p.Name
							}
							return names
						}())
				}
			}
		}
	}

	return nil
}

func isDynamicOrVirtualTableName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if strings.ContainsAny(name, "{}") {
		return true
	}
	for _, fn := range []string{"json_each", "json_tree", "json_each_text", "json_tree_text"} {
		if strings.HasPrefix(name, fn) {
			return true
		}
	}
	return false
}

// isParamNullable checks if a param's corresponding schema column is nullable.
func (p *QueryParser) isParamNullable(paramName string, table *Table) bool {
	if table == nil {
		return false
	}
	baseName := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(
		strings.TrimSuffix(paramName, "_start"), "_end"), "_delta"), "_prefix")
	for _, col := range table.Columns {
		if strings.EqualFold(col.Name, baseName) {
			return col.Nullable
		}
	}
	return false
}

func (p *QueryParser) isCQLProvider() bool {
	if p.Config == nil {
		return false
	}
	prov := p.Config.Database.Provider
	return prov == "scylla" || prov == "scylladb" || prov == "cassandra"
}
