package parser

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

type TypeInferrer struct {
	mu     sync.RWMutex
	cache  map[string]string
	schema *Schema
}

func NewTypeInferrer() *TypeInferrer {
	return &TypeInferrer{cache: make(map[string]string, 64)}
}

func NewTypeInferrerWithSchema(schema *Schema) *TypeInferrer {
	return &TypeInferrer{cache: make(map[string]string, 64), schema: schema}
}

// InferParamTypeByName infers a param type from its name alone, without a table schema.
// Used for CTE queries where the table can't be resolved.
func (ti *TypeInferrer) InferParamTypeByName(paramName string) string {
	n := strings.ToLower(paramName)
	switch n {
	case "limit", "offset":
		return "BIGINT"
	case "count", "min_count", "count_threshold":
		return "BIGINT"
	case "id", "age":
		return "INTEGER"
	}
	if strings.HasSuffix(n, "_count") || strings.HasSuffix(n, "_sum") ||
		strings.HasSuffix(n, "_total") || strings.HasSuffix(n, "_num") {
		return "BIGINT"
	}
	if strings.HasSuffix(n, "_age") {
		return "INTEGER"
	}
	// _id suffix is not assumed INTEGER — it could be UUID.
	// The schema-based lookup in inferParamTypeInternal handles _id columns.
	if strings.Contains(n, "score") || strings.Contains(n, "rating") || strings.Contains(n, "avg") {
		return "DOUBLE PRECISION"
	}
	if strings.Contains(n, "is_") || strings.HasPrefix(n, "is_") || n == "active" || n == "featured" || n == "pinned" {
		return "BOOLEAN"
	}
	return "TEXT"
}

func (ti *TypeInferrer) InferParamType(sql string, paramIndex int, table *Table, paramName string) string {
	cacheKey := fmt.Sprintf("%s:%d:%s", table.Name, paramIndex, paramName)

	ti.mu.RLock()
	cached, ok := ti.cache[cacheKey]
	ti.mu.RUnlock()
	if ok {
		return cached
	}

	result := ti.inferParamTypeInternal(sql, paramIndex, table, paramName)

	if result != "" {
		ti.mu.Lock()
		ti.cache[cacheKey] = result
		ti.mu.Unlock()
	}

	return result
}

func (ti *TypeInferrer) inferParamTypeInternal(sql string, paramIndex int, table *Table, paramName string) string {
	// Well-known param names that always have fixed types (non-column names)
	nameLower := strings.ToLower(paramName)
	switch nameLower {
	case "limit", "offset":
		return "BIGINT"
	case "count", "min_count", "count_threshold":
		return "BIGINT"
	}
	// col = ANY($N) must be checked before name-based lookup, as the param name
	// is the column name but the type is col.Type[] (an array), not col.Type.
	anyArrayRe := regexp.MustCompile(fmt.Sprintf(`(?i)(?:\w+\.)?(\w+)\s*=\s*ANY\s*\(\s*\$%d`, paramIndex))
	if match := anyArrayRe.FindStringSubmatch(sql); len(match) > 1 {
		for _, col := range table.Columns {
			if strings.EqualFold(col.Name, match[1]) {
				return col.Type + "[]"
			}
		}
	}

	// $N = ANY(col) or $N = ANY(alias.col) — reverse form: param is a scalar element
	// being checked for membership in an array column. Type is the element type.
	anyRevArrayRe := regexp.MustCompile(fmt.Sprintf(`(?i)\$%d\s*=\s*ANY\s*\(\s*(?:(\w+)\.)?(\w+)\s*\)`, paramIndex))
	if match := anyRevArrayRe.FindStringSubmatch(sql); len(match) > 2 {
		colName := match[2]
		// Search primary table
		for _, col := range table.Columns {
			if strings.EqualFold(col.Name, colName) {
				elemType := strings.TrimSuffix(col.Type, "[]")
				return elemType
			}
		}
		// Cross-table lookup for qualified references
		if ti.schema != nil {
			for _, t := range ti.schema.Tables {
				for _, col := range t.Columns {
					if strings.EqualFold(col.Name, colName) {
						elemType := strings.TrimSuffix(col.Type, "[]")
						return elemType
					}
				}
			}
			// Fallback: if colName is a plural (e.g., "users"), try singular + "_id" form
			// (e.g., "user_id") since subquery aliases often pluralize the source column.
			singular := strings.TrimSuffix(colName, "s")
			singularID := singular + "_id"
			for _, t := range ti.schema.Tables {
				for _, col := range t.Columns {
					if strings.EqualFold(col.Name, singularID) {
						return col.Type
					}
				}
			}
		}
	}

	if paramName != "" && paramName != fmt.Sprintf("param%d", paramIndex) {
		for _, col := range table.Columns {
			if strings.EqualFold(col.Name, paramName) ||
				strings.EqualFold(col.Name, strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(paramName, "_start"), "_end"), "_delta"), "_prefix")) {
				return col.Type
			}
		}
		// Cross-table lookup: param name may refer to a column in another table (subquery joins)
		if ti.schema != nil {
			baseName := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(paramName, "_start"), "_end"), "_delta"), "_prefix")
			for _, t := range ti.schema.Tables {
				for _, col := range t.Columns {
					if strings.EqualFold(col.Name, baseName) {
						return col.Type
					}
				}
			}
		}
	}

	// Any param named *_count, *_sum, *_total — these are typically compared to
	// COUNT/SUM results which return BIGINT in PostgreSQL. Runs AFTER the schema
	// column lookups above, so a real column named e.g. view_count keeps its
	// declared INTEGER type.
	if strings.HasSuffix(nameLower, "_count") || strings.HasSuffix(nameLower, "_sum") ||
		strings.HasSuffix(nameLower, "_total") || strings.HasSuffix(nameLower, "_num") {
		return "BIGINT"
	}

	aggregatePattern := fmt.Sprintf(`(?i)\b(count|sum|avg|max|min|total)_?\w*\s*[<>=!]+\s*\$%d|\$%d\s*[<>=!]+\s*\b(count|sum|avg|max|min|total)_?\w*|\b\w+_count\b\s*[<>=!]+\s*\$%d|\b\w+_sum\b\s*[<>=!]+\s*\$%d`, paramIndex, paramIndex, paramIndex, paramIndex)
	if matched, _ := regexp.MatchString(aggregatePattern, sql); matched {
		return "INTEGER"
	}

	// CTE alias numeric column comparisons: ups.total_posts > $1
	numericAliasPattern := fmt.Sprintf(`(?i)\w+\.(total_posts|published_posts|draft_posts|total_comments|posts_commented_on|categories_used|engagement_score|count|sum|avg|total|min|max|num|qty|quantity|amount|cnt|total_cnt|post_cnt|comment_cnt|pub_cnt|draft_cnt|posts_cnt|cat_cnt)\s*[<>=!]+\s*\$%d|\$%d\s*[<>=!]+\s*\w+\.(total_posts|published_posts|draft_posts|total_comments|posts_commented_on|categories_used|engagement_score|count|sum|avg|total|min|max|num|qty|quantity|amount|cnt)`, paramIndex, paramIndex)
	if matched, _ := regexp.MatchString(numericAliasPattern, sql); matched {
		return "INTEGER"
	}

	coalescePattern := fmt.Sprintf(`(?i)COALESCE\([^)]*\.(cnt|count|sum|avg|total|total_\w+|post\w*|comment\w*|pub\w*|draft\w*|posts\w*|cat\w*|unique\w*|engagement\w*|categories\w*)[^)]*\)\s*[<>=!]+\s*\$%d|\$%d\s*[<>=!]+\s*COALESCE\([^)]*\.(cnt|count|sum|avg|total)`, paramIndex, paramIndex)
	if matched, _ := regexp.MatchString(coalescePattern, sql); matched {
		return "INTEGER"
	}

	wherePattern := fmt.Sprintf(`(?i)WHERE\s+(?:\w+\.)?(\w+)\s*=\s*\$%d`, paramIndex)
	whereRe := regexp.MustCompile(wherePattern)
	if match := whereRe.FindStringSubmatch(sql); len(match) > 1 {
		for _, col := range table.Columns {
			if strings.EqualFold(col.Name, match[1]) {
				return col.Type
			}
		}
		// Cross-table lookup for CTE/subquery contexts where the column belongs to another table
		if ti.schema != nil {
			for _, t := range ti.schema.Tables {
				for _, col := range t.Columns {
					if strings.EqualFold(col.Name, match[1]) {
						return col.Type
					}
				}
			}
		}
	}

	// func(col) = $N pattern
	funcColTypeRe := regexp.MustCompile(fmt.Sprintf(`(?i)(?:WHERE|AND|OR)\s*\(?\s*\w+\s*\(\s*(?:\w+\.)?(\w+)\s*\)(?:::\w+)?\s*=\s*\$%d\b`, paramIndex))
	if match := funcColTypeRe.FindStringSubmatch(sql); len(match) > 1 {
		for _, col := range table.Columns {
			if strings.EqualFold(col.Name, match[1]) {
				return col.Type
			}
		}
	}

	// ILIKE / SIMILAR TO / LIKE patterns: WHERE col ILIKE $N or col ILIKE '%' || $N || '%'
	likePattern := fmt.Sprintf(`(?i)(?:\w+\.)?(\w+)\s+(?:I?LIKE|SIMILAR\s+TO|NOT\s+I?LIKE)\s+\S*\$%d\b`, paramIndex)
	likeRe := regexp.MustCompile(likePattern)
	if match := likeRe.FindStringSubmatch(sql); len(match) > 1 {
		for _, col := range table.Columns {
			if strings.EqualFold(col.Name, match[1]) {
				return col.Type
			}
		}
		return "TEXT" // default for LIKE params
	}

	// Interval expression: ($N || ' days')::INTERVAL — param is an integer (number of days/hours/etc.)
	intervalTypeRe := regexp.MustCompile(fmt.Sprintf(`(?i)\(\s*\$%d\s*\|\|\s*'[^']*'\s*\)\s*::\s*INTERVAL`, paramIndex))
	if intervalTypeRe.MatchString(sql) {
		return "INTEGER"
	}
	intervalTypeRe2 := regexp.MustCompile(fmt.Sprintf(`(?i)INTERVAL\s+\$%d\b`, paramIndex))
	if intervalTypeRe2.MatchString(sql) {
		return "INTEGER"
	}

	if strings.Contains(strings.ToUpper(sql), "INSERT") {
		insertColRegex := regexp.MustCompile(`(?i)INSERT\s+INTO\s+\S+\s*\(([\s\S]*?)\)\s*VALUES`)
		allInsertCols := []string{}
		for _, match := range insertColRegex.FindAllStringSubmatch(sql, -1) {
			for c := range strings.SplitSeq(match[1], ",") {
				allInsertCols = append(allInsertCols, strings.TrimSpace(c))
			}
		}
		if paramIndex <= len(allInsertCols) {
			colName := allInsertCols[paramIndex-1]
			for _, col := range table.Columns {
				if strings.EqualFold(col.Name, colName) {
					return col.Type
				}
			}
		}
	}

	setPattern := fmt.Sprintf(`(?i)SET\s+(\w+)\s*=\s*\$%d`, paramIndex)
	setRe := regexp.MustCompile(setPattern)
	if match := setRe.FindStringSubmatch(sql); len(match) > 1 {
		for _, col := range table.Columns {
			if strings.EqualFold(col.Name, match[1]) {
				return col.Type
			}
		}
	}
	// SET col = COALESCE($N, col)
	setCoalesceRe := regexp.MustCompile(fmt.Sprintf(`(?i)(\w+)\s*=\s*COALESCE\s*\(\s*\$%d\b`, paramIndex))
	if match := setCoalesceRe.FindStringSubmatch(sql); len(match) > 1 {
		for _, col := range table.Columns {
			if strings.EqualFold(col.Name, match[1]) {
				return col.Type
			}
		}
	}
	// SET with ? params — extract by using same logic as InferParamName
	if strings.Contains(sql, "?") {
		setColPattern := regexp.MustCompile(`(?i)SET\s+([\s\S]*?)(?:WHERE|$)`)
		if setMatch := setColPattern.FindStringSubmatch(sql); len(setMatch) > 1 {
			colPattern := regexp.MustCompile(`(\w+)\s*=\s*\?`)
			matches := colPattern.FindAllStringSubmatch(setMatch[1], -1)
			if paramIndex <= len(matches) {
				colName := matches[paramIndex-1][1]
				for _, col := range table.Columns {
					if strings.EqualFold(col.Name, colName) {
						return col.Type
					}
				}
			}
		}
	}

	limitPattern := fmt.Sprintf(`(?i)LIMIT\s+\$%d`, paramIndex)
	if matched, _ := regexp.MatchString(limitPattern, sql); matched {
		return "BIGINT"
	}

	offsetPattern := fmt.Sprintf(`(?i)OFFSET\s+\$%d`, paramIndex)
	if matched, _ := regexp.MatchString(offsetPattern, sql); matched {
		return "BIGINT"
	}

	betweenPattern := fmt.Sprintf(`(?i)(\w+)\s+BETWEEN\s+\$%d`, paramIndex)
	betweenRe := regexp.MustCompile(betweenPattern)
	if match := betweenRe.FindStringSubmatch(sql); len(match) > 1 {
		for _, col := range table.Columns {
			if strings.EqualFold(col.Name, match[1]) {
				return col.Type
			}
		}
	}

	betweenEndPattern := fmt.Sprintf(`(?i)BETWEEN\s+\$\d+\s+AND\s+\$%d`, paramIndex)
	if matched, _ := regexp.MatchString(betweenEndPattern, sql); matched {
		betweenStartRe := regexp.MustCompile(`(?i)(\w+)\s+BETWEEN`)
		if match := betweenStartRe.FindStringSubmatch(sql); len(match) > 1 {
			for _, col := range table.Columns {
				if strings.EqualFold(col.Name, match[1]) {
					return col.Type
				}
			}
		}
	}

	datePattern := fmt.Sprintf(`(?i)(created_at|updated_at|deleted_at|published_at|date|time)\s*[<>=]+\s*\$%d`, paramIndex)
	if matched, _ := regexp.MatchString(datePattern, sql); matched {
		return "TIMESTAMP"
	}

	// WHERE alias.col > $N — unqualified comparison fallback in primary table
	compQualPattern := fmt.Sprintf(`(?i)(?:(\w+)\.)?(\w+)\s*[<>=!]+\s*\$%d`, paramIndex)
	compQualRe := regexp.MustCompile(compQualPattern)
	if match := compQualRe.FindStringSubmatch(sql); len(match) > 1 {
		tableQual := match[1]
		colName := match[2]
		// Search primary table first
		for _, col := range table.Columns {
			if strings.EqualFold(col.Name, colName) {
				return col.Type
			}
		}
		// Cross-table lookup
		if ti.schema != nil {
			for _, t := range ti.schema.Tables {
				for _, col := range t.Columns {
					if strings.EqualFold(col.Name, colName) {
						return col.Type
					}
				}
			}
			// CTE resolution: ct.depth — if there's a table qualifier, try resolving via CTE
			if tableQual != "" {
				if cteType, _, found := ti.resolveCTEColumn(sql, tableQual, colName); found {
					return cteType
				}
			}
		}
	}

	return "TEXT"
}
