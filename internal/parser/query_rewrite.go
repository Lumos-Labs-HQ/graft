package parser

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// stripIdentQuotes removes surrounding double-quotes and backticks from identifiers.
func stripIdentQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '`' && s[len(s)-1] == '`') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// matchesTableName compares a schema table name with a query table reference.
// Handles keyspace-qualified names: "ks"."tbl" = ks.tbl
func matchesTableName(schemaName, queryName string) bool {
	// Exact match
	if strings.EqualFold(schemaName, queryName) {
		return true
	}
	// If the query reference is ks.tbl, extract just the table part and match
	if _, tbl, ok := strings.CutLast(queryName, "."); ok {
		// Match against plain table name
		if strings.EqualFold(schemaName, tbl) {
			return true
		}
		// Match against ks.tbl form
		if strings.EqualFold(schemaName, queryName) {
			return true
		}
	}
	// Schema name might be ks.tbl, query might be plain tbl
	if _, tbl, ok := strings.CutLast(schemaName, "."); ok {
		if strings.EqualFold(tbl, queryName) {
			return true
		}
	}
	return false
}

// extractBalancedParens extracts the content between the opening paren at startPos
// and its matching closing paren, returning the inner content without the parens.
func extractBalancedParens(s string, startPos int) string {
	if startPos >= len(s) || s[startPos] != '(' {
		return ""
	}
	depth := 0
	for i := startPos; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[startPos+1 : i]
			}
		}
	}
	return ""
}

// renumberParams rewrites $N placeholders from original numbers to sequential
func renumberParams(sql string, orderedNums []int) string {
	mapping := make(map[int]int, len(orderedNums))
	for i, orig := range orderedNums {
		mapping[orig] = i + 1
	}
	re := regexp.MustCompile(`\$(\d+)`)
	return re.ReplaceAllStringFunc(sql, func(match string) string {
		var n int
		if _, err := fmt.Sscanf(match[1:], "%d", &n); err != nil {
			return match
		}
		if newNum, ok := mapping[n]; ok {
			return fmt.Sprintf("$%d", newNum)
		}
		return match
	})
}

// extractOrderedParamNums returns deduped ordered $N numbers from SQL.
func extractOrderedParamNums(sql string) []int {
	re := regexp.MustCompile(`\$(\d+)`)
	matches := re.FindAllStringSubmatch(sql, -1)
	seen := make(map[int]bool, len(matches))
	result := make([]int, 0, len(matches))
	for _, m := range matches {
		if len(m) > 1 {
			var n int
			if _, err := fmt.Sscanf(m[1], "%d", &n); err != nil {
				continue
			}
			if !seen[n] {
				seen[n] = true
				result = append(result, n)
			}
		}
	}
	return result
}

// rewriteINListToANY rewrites `col IN ($1, $2, $3)` → `col = ANY($1)` and
// renumbers all subsequent $N params so they stay sequential.
func rewriteINListToANY(sql string) string {
	inRe := regexp.MustCompile(`(?i)(\w+)\s+IN\s*\(\s*(\$\d+(?:\s*,\s*\$\d+)*)\s*\)`)
	numRe := regexp.MustCompile(`\$(\d+)`)

	type inSpan struct {
		start, end int
		col        string
		nums       []int // original $N numbers in this IN list
	}

	var spans []inSpan
	for _, loc := range inRe.FindAllStringSubmatchIndex(sql, -1) {
		paramsStr := sql[loc[4]:loc[5]]
		var nums []int
		for _, m := range numRe.FindAllStringSubmatch(paramsStr, -1) {
			var n int
			_, _ = fmt.Sscanf(m[1], "%d", &n)
			nums = append(nums, n)
		}
		if len(nums) < 2 {
			continue
		}
		spans = append(spans, inSpan{loc[0], loc[1], sql[loc[2]:loc[3]], nums})
	}
	if len(spans) == 0 {
		return sql
	}

	// Build a remapping: original $N → new $N
	// Each IN list keeps only its first param; the rest are removed.
	// Collect all "removed" param numbers in sorted order.
	removed := map[int]bool{}
	for _, s := range spans {
		for _, n := range s.nums[1:] {
			removed[n] = true
		}
	}

	// For each original $N, compute its new number (subtract count of removed nums < N)
	newNum := func(orig int) int {
		shift := 0
		for r := range removed {
			if r < orig {
				shift++
			}
		}
		return orig - shift
	}

	// Replace spans in reverse order (to preserve offsets). Keep each IN list's
	// ORIGINAL first param number here; the single renumber pass below remaps
	// every surviving $N — including these — to its final sequential value.
	for _, s := range slices.Backward(spans) {

		replacement := fmt.Sprintf("%s = ANY($%d)", s.col, s.nums[0])
		sql = sql[:s.start] + replacement + sql[s.end:]
	}

	// Renumber every remaining $N in a single pass. Doing this with per-number
	// ReplaceAll calls is unsafe: remapping $4->$3 and then $3->$2 also rewrites
	// the $3 the first step just produced. ReplaceAllStringFunc visits each
	// original token exactly once, so newNum is applied to originals only.
	sql = numRe.ReplaceAllStringFunc(sql, func(m string) string {
		var n int
		if _, err := fmt.Sscanf(m[1:], "%d", &n); err != nil {
			return m
		}
		return fmt.Sprintf("$%d", newNum(n))
	})

	return sql
}

// attachJsonTypesToQuery links @json type definitions to matching query columns and params.
// JSON types are stored on the query for data class generation, and columns get typed fields.
// Params matching a JSON column name get their type marked for serialization.
func attachJsonTypesToQuery(q *Query) {
	if len(q.JsonTypes) == 0 {
		return
	}

	// Build lookup map
	jsonMap := make(map[string]*JsonType, len(q.JsonTypes))
	for _, jt := range q.JsonTypes {
		jsonMap[strings.ToLower(jt.Column)] = jt
	}

	// Attach to return columns
	for _, col := range q.Columns {
		if jt, ok := jsonMap[strings.ToLower(col.Name)]; ok {
			col.JsonDef = jt
		}
	}

	// Mark params that correspond to JSON columns for serialization
	for _, param := range q.Params {
		if jt, ok := jsonMap[strings.ToLower(param.Name)]; ok {
			param.Type = "@json:" + jt.Name
		}
	}
}
