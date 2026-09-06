package rustgen

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/Lumos-Labs-HQ/flash/internal/gencommon"
	"github.com/Lumos-Labs-HQ/flash/internal/parser"
	"github.com/Lumos-Labs-HQ/flash/internal/utils"
)

type fileGroup struct {
	src     string
	queries []*parser.Query
}

// sharedStructPlan deduplicates structurally identical Row/Params structs.
// The first occurrence in deterministic order (sorted source files, in-file
// query order) is emitted as the canonical struct; later occurrences become
// `pub type X = Y;` aliases. Row and Params shapes are deduped within their
// own namespaces — a Row never aliases a Params.
type sharedStructPlan struct {
	// rowNames/paramsNames map a query to its final (suffix-deduped) struct
	// name, allocated deterministically before parallel generation starts.
	rowNames    map[*parser.Query]string
	paramsNames map[*parser.Query]string
	// canonical maps shape key → canonical struct name.
	canonical map[string]string
	// alias maps struct name → canonical struct name (identity for the
	// canonical struct itself).
	alias map[string]string
	// ownerSrc maps canonical struct name → source file owning the definition.
	ownerSrc map[string]string
	// imports maps source file → canonical struct names it must import from
	// other modules for its aliases.
	imports map[string]map[string]bool
}

func (p *sharedStructPlan) addImport(src, canonicalName string) {
	if p.imports[src] == nil {
		p.imports[src] = make(map[string]bool)
	}
	p.imports[src][canonicalName] = true
}

// planSharedStructs precomputes deterministic struct names and the canonical/
// alias split for all query files. It must mirror the emission conditions in
// generateSingleRustFile exactly: a query emits a Row struct when it is not an
// exec command, expands to >1 column, and does not match a table model; a
// Params struct when it has >2 params.
func (g *Generator) planSharedStructs(groups []fileGroup) *sharedStructPlan {
	plan := &sharedStructPlan{
		rowNames:    make(map[*parser.Query]string),
		paramsNames: make(map[*parser.Query]string),
		canonical:   make(map[string]string),
		alias:       make(map[string]string),
		ownerSrc:    make(map[string]string),
		imports:     make(map[string]map[string]bool),
	}

	sorted := make([]fileGroup, len(groups))
	copy(sorted, groups)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].src < sorted[j].src })

	usedNames := make(map[string]int)

	claim := func(base string) string {
		if count, ok := usedNames[base]; ok {
			usedNames[base] = count + 1
			return fmt.Sprintf("%s%d", base, count+1)
		}
		usedNames[base] = 1
		return base
	}

	for _, fg := range sorted {
		for _, q := range fg.queries {
			cmd := strings.ToLower(q.Cmd)
			isExec := cmd == ":exec" || cmd == "exec" || cmd == ":execresult" || cmd == "execresult"
			if !isExec {
				columns := g.expandWildcardColumns(q)
				if len(columns) > 1 && g.modelTypeForQuery(q, columns) == "" {
					name := claim(toRustStructName(q.Name) + "Row")
					plan.rowNames[q] = name
					shape := "R" + g.rowShapeKey(columns)
					plan.assign(shape, name, fg.src)
				}
			}
			if len(q.Params) > 2 {
				name := toRustStructName(q.Name) + "Params"
				plan.paramsNames[q] = name
				shape := "P" + g.paramsShapeKey(q)
				plan.assign(shape, name, fg.src)
			}
		}
	}
	return plan
}

func (p *sharedStructPlan) assign(shape, name, src string) {
	if canon, ok := p.canonical[shape]; ok {
		p.alias[name] = canon
		if p.ownerSrc[canon] != src {
			p.addImport(src, canon)
		}
		return
	}
	p.canonical[shape] = name
	p.alias[name] = name
	p.ownerSrc[name] = src
}

// rowShapeKey builds the structural identity of a row struct: ordered
// (name, rust type) pairs. Identical column lists in different order are NOT
// the same struct (fields are emitted in SELECT order).
func (g *Generator) rowShapeKey(columns []*parser.QueryColumn) string {
	var b strings.Builder
	for _, col := range columns {
		b.WriteString("|")
		b.WriteString(utils.ToSnakeCase(col.Name))
		b.WriteString(":")
		b.WriteString(g.rowFieldType(col))
	}
	return b.String()
}

func (g *Generator) paramsShapeKey(q *parser.Query) string {
	var b strings.Builder
	for _, p := range q.Params {
		b.WriteString("|")
		b.WriteString(utils.ToSnakeCase(p.Name))
		b.WriteString(":")
		b.WriteString(g.sqlTypeToRust(p.Type, false))
	}
	return b.String()
}

// rowFieldType computes the emitted Rust type for a result column, applying
// the macros-mode nullability adjustments. Shared by shape keys and emission
// so the two can never drift apart.
func (g *Generator) rowFieldType(col *parser.QueryColumn) string {
	nullable := col.Nullable
	if g.Config.Gen.Rust.Macros {
		// sqlx macros always infer computed/aggregate columns as nullable
		if col.IsComputed {
			nullable = true
		}
		// sqlx macros return Option<i64> for COUNT/SUM/RANK results
		// even when accessed through CTE aliases or JOINs.
		// Any BIGINT that isn't an actual table column should be Option.
		if strings.ToUpper(col.Type) == "BIGINT" && !g.isActualTableColumn(col.Name, col.Table) {
			nullable = true
		}
	}
	return g.sqlTypeToRust(col.Type, nullable)
}

// rowTypeName resolves the struct name a query's method returns. Falls back
// to the plain derived name when no plan is active (e.g. cache-only tests).
func (g *Generator) rowTypeName(q *parser.Query) string {
	if g.structPlan != nil {
		if name, ok := g.structPlan.rowNames[q]; ok {
			return name
		}
	}
	return toRustStructName(q.Name) + "Row"
}

func (g *Generator) paramsTypeName(q *parser.Query) string {
	if g.structPlan != nil {
		if name, ok := g.structPlan.paramsNames[q]; ok {
			return name
		}
	}
	return toRustStructName(q.Name) + "Params"
}

func (g *Generator) generateQueriesIncremental(queries []*parser.Query, fullRegen bool) error {
	queryGroups := make(map[string][]*parser.Query)
	for _, q := range queries {
		src := q.SourceFile
		if src == "" {
			src = "queries"
		}
		queryGroups[src] = append(queryGroups[src], q)
	}

	groups := make([]fileGroup, 0, len(queryGroups))
	for src, qs := range queryGroups {
		groups = append(groups, fileGroup{src, qs})
	}

	// Precompute struct names and the canonical/alias split deterministically
	// before parallel generation starts, so identical structs are emitted once.
	g.structPlan = g.planSharedStructs(groups)

	numWorkers := min(runtime.NumCPU(), len(groups))
	if numWorkers < 1 {
		numWorkers = 1
	}

	workCh := make(chan fileGroup, len(groups))
	errCh := make(chan error, len(groups))
	var wg sync.WaitGroup

	for i := 0; i < numWorkers; i++ {
		wg.Go(func() {
			for fg := range workCh {
				if err := g.generateSingleRustFile(fg.src, fg.queries, fullRegen); err != nil {
					errCh <- err
				}
			}
		})
	}
	for _, fg := range groups {
		workCh <- fg
	}
	close(workCh)
	go func() { wg.Wait(); close(errCh) }()
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func (g *Generator) generateSingleRustFile(src string, queries []*parser.Query, fullRegen bool) error {
	queryFile := filepath.Join(g.Config.Queries, src+".sql")
	currentHash, _ := gencommon.ComputeFileChecksum(queryFile)

	// The skip decision must consider this generator's own output file so a
	// shared cache entry from another language cannot suppress generation.
	primaryOutput := filepath.Join(g.Config.Gen.Rust.Out, toRustModuleName(src)+".rs")
	if !gencommon.ShouldRegenerateFileForOutput(g.cache, queryFile, currentHash, fullRegen, primaryOutput) {
		gencommon.PrintSkipMessage(src, ".rs")
		return nil
	}
	gencommon.PrintGenerateMessage(src, ".rs")

	w := gencommon.GetBuilder()
	defer gencommon.PutBuilder(w)

	w.WriteString("// Code generated by FlashORM. DO NOT EDIT.\n")
	w.WriteString("#![allow(unused_imports, dead_code)]\n\n")

	// Collect imports needed for this file
	imports := g.collectImports(queries)
	for _, imp := range imports {
		w.WriteString(fmt.Sprintf("use %s;\n", imp))
	}
	if len(imports) > 0 {
		w.WriteString("\n")
	}

	w.WriteString("use super::models::*;\n")
	w.WriteString("use super::db::Queries;\n")

	// Canonical structs aliased from other modules need explicit imports
	// (explicit imports shadow the glob above).
	if g.structPlan != nil {
		if imported := g.structPlan.imports[src]; len(imported) > 0 {
			names := make([]string, 0, len(imported))
			for name := range imported {
				names = append(names, name)
			}
			slices.Sort(names)
			for _, name := range names {
				ownerMod := toRustModuleName(g.structPlan.ownerSrc[name])
				w.WriteString(fmt.Sprintf("use super::%s::%s;\n", ownerMod, name))
			}
		}
	}
	w.WriteString("\n")

	useMacros := g.Config.Gen.Rust.Macros

	// Generate row structs for queries returning multiple columns not matching a model.
	// Structurally identical structs are emitted once; duplicates become type aliases.
	for _, q := range queries {
		cmd := strings.ToLower(q.Cmd)
		if cmd == ":exec" || cmd == "exec" || cmd == ":execresult" || cmd == "execresult" {
			continue
		}
		columns := g.expandWildcardColumns(q)
		if len(columns) == 0 {
			continue
		}
		modelType := g.modelTypeForQuery(q, columns)
		if modelType != "" {
			continue
		}
		if len(columns) == 1 {
			continue
		}

		structName := g.rowTypeName(q)
		if g.structPlan != nil {
			if canon, ok := g.structPlan.alias[structName]; ok && canon != structName {
				w.WriteString(fmt.Sprintf("pub type %s = %s;\n\n", structName, canon))
				continue
			}
		}

		if useMacros {
			// With macros, FromRow is not needed — the macro handles mapping
			w.WriteString("#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]\n")
		} else {
			w.WriteString("#[derive(Debug, Clone, sqlx::FromRow, serde::Serialize, serde::Deserialize)]\n")
		}
		w.WriteString(fmt.Sprintf("pub struct %s {\n", structName))
		for _, col := range columns {
			fieldName := utils.ToSnakeCase(col.Name)
			rustType := g.rowFieldType(col)
			w.WriteString(fmt.Sprintf("    pub %s: %s,\n", fieldName, rustType))
		}
		w.WriteString("}\n\n")
	}

	// Generate param structs for queries with >2 params.
	// Structurally identical params structs are emitted once; duplicates become type aliases.
	for _, q := range queries {
		if len(q.Params) <= 2 {
			continue
		}
		structName := g.paramsTypeName(q)
		if g.structPlan != nil {
			if canon, ok := g.structPlan.alias[structName]; ok && canon != structName {
				w.WriteString(fmt.Sprintf("pub type %s = %s;\n\n", structName, canon))
				continue
			}
		}
		w.WriteString(fmt.Sprintf("pub struct %s {\n", structName))
		for _, p := range q.Params {
			fieldName := utils.ToSnakeCase(p.Name)
			rustType := g.sqlTypeToRust(p.Type, false)
			w.WriteString(fmt.Sprintf("    pub %s: %s,\n", fieldName, rustType))
		}
		w.WriteString("}\n\n")
	}

	// Generate impl block for Queries
	w.WriteString("impl Queries {\n")
	for _, q := range queries {
		g.generateQueryMethod(w, q)
	}
	w.WriteString("}\n")

	// Write output
	modName := toRustModuleName(src)
	outPath := filepath.Join(g.Config.Gen.Rust.Out, modName+".rs")
	if err := os.WriteFile(outPath, []byte(w.String()), 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", outPath, err)
	}

	// Update cache
	g.cache.UpdateQueryChecksum(queryFile, currentHash)
	deps := gencommon.ExtractTableDependencies(queries)
	g.cache.UpdateQueryDependencies(queryFile, deps)

	return nil
}

func (g *Generator) generateQueryMethod(w *strings.Builder, q *parser.Query) {
	fnName := toRustFnName(q.Name)
	columns := g.expandWildcardColumns(q)

	cmd := strings.ToLower(q.Cmd)
	useMacros := g.Config.Gen.Rust.Macros

	// Determine return type
	var returnType string
	var isExec bool
	switch cmd {
	case ":exec", "exec":
		returnType = "()"
		isExec = true
	case ":execresult", "execresult":
		returnType = g.getExecResultType()
		isExec = true
	case ":one", "one":
		if useMacros && len(columns) == 1 {
			// sqlx::query_scalar! always returns Option<T>
			returnType = g.getMacroScalarReturnType(columns[0])
		} else {
			returnType = g.getOneReturnType(q, columns)
		}
	case ":many", "many":
		if useMacros && len(columns) == 1 {
			// sqlx::query_scalar! always returns Option<T> per element
			returnType = fmt.Sprintf("Vec<%s>", g.getMacroScalarReturnType(columns[0]))
		} else {
			returnType = fmt.Sprintf("Vec<%s>", g.getOneReturnType(q, columns))
		}
	default:
		returnType = "()"
		isExec = true
	}

	// Build function signature
	w.WriteString(fmt.Sprintf("    /// %s\n", q.Name))
	w.WriteString(fmt.Sprintf("    pub async fn %s(\n        &self,\n", fnName))

	// Parameters
	if len(q.Params) > 2 {
		paramsStruct := g.paramsTypeName(q)
		w.WriteString(fmt.Sprintf("        params: &%s,\n", paramsStruct))
	} else {
		for _, p := range q.Params {
			paramName := utils.ToSnakeCase(p.Name)
			paramType := g.paramTypeToRust(p.Type)
			w.WriteString(fmt.Sprintf("        %s: %s,\n", paramName, paramType))
		}
	}
	w.WriteString(fmt.Sprintf("    ) -> Result<%s, sqlx::Error> {\n", returnType))

	// Generate body — choose between macros and runtime functions
	sql := g.convertSQL(q.SQL)

	if g.Config.Gen.Rust.Macros {
		if isExec {
			g.writeMacroExecBody(w, q, sql, cmd)
		} else if cmd == ":one" || cmd == "one" {
			g.writeMacroOneBody(w, q, sql, columns)
		} else {
			g.writeMacroManyBody(w, q, sql, columns)
		}
	} else {
		if isExec {
			g.writeExecBody(w, q, sql, cmd)
		} else if cmd == ":one" || cmd == "one" {
			g.writeOneBody(w, q, sql, columns)
		} else {
			g.writeManyBody(w, q, sql, columns)
		}
	}

	w.WriteString("    }\n\n")
}

func (g *Generator) writeExecBody(w *strings.Builder, q *parser.Query, sql string, cmd string) {
	w.WriteString(fmt.Sprintf("        sqlx::query(\n            \"%s\"\n        )\n", escapeSQLForRust(sql)))
	g.writeBinds(w, q)
	if cmd == ":execresult" || cmd == "execresult" {
		w.WriteString("        .execute(&self.pool)\n        .await\n")
	} else {
		w.WriteString("        .execute(&self.pool)\n        .await?;\n        Ok(())\n")
	}
}

func (g *Generator) writeOneBody(w *strings.Builder, q *parser.Query, sql string, columns []*parser.QueryColumn) {
	returnType := g.getOneReturnType(q, columns)
	if len(columns) == 1 {
		// Single column: use query_scalar with DB type
		w.WriteString(fmt.Sprintf("        sqlx::query_scalar::<%s, %s>(\n            \"%s\"\n        )\n", g.getDBType(), returnType, escapeSQLForRust(sql)))
		g.writeBinds(w, q)
		w.WriteString("        .fetch_one(&self.pool)\n        .await\n")
	} else {
		// Multi column: use query_as with FromRow
		w.WriteString(fmt.Sprintf("        sqlx::query_as::<_, %s>(\n            \"%s\"\n        )\n", returnType, escapeSQLForRust(sql)))
		g.writeBinds(w, q)
		w.WriteString("        .fetch_one(&self.pool)\n        .await\n")
	}
}

func (g *Generator) writeManyBody(w *strings.Builder, q *parser.Query, sql string, columns []*parser.QueryColumn) {
	oneType := g.getOneReturnType(q, columns)
	if len(columns) == 1 {
		// Single column: use query_scalar with DB type
		w.WriteString(fmt.Sprintf("        sqlx::query_scalar::<%s, %s>(\n            \"%s\"\n        )\n", g.getDBType(), oneType, escapeSQLForRust(sql)))
		g.writeBinds(w, q)
		w.WriteString("        .fetch_all(&self.pool)\n        .await\n")
	} else {
		// Multi column: use query_as with FromRow
		w.WriteString(fmt.Sprintf("        sqlx::query_as::<_, %s>(\n            \"%s\"\n        )\n", oneType, escapeSQLForRust(sql)))
		g.writeBinds(w, q)
		w.WriteString("        .fetch_all(&self.pool)\n        .await\n")
	}
}

func (g *Generator) writeBinds(w *strings.Builder, q *parser.Query) {
	if len(q.Params) > 2 {
		for _, p := range q.Params {
			fieldName := utils.ToSnakeCase(p.Name)
			w.WriteString(fmt.Sprintf("        .bind(&params.%s)\n", fieldName))
		}
	} else {
		for _, p := range q.Params {
			paramName := utils.ToSnakeCase(p.Name)
			w.WriteString(fmt.Sprintf("        .bind(%s)\n", paramName))
		}
	}
}

// macroArgs builds the comma-separated argument list for sqlx macros
func (g *Generator) macroArgs(q *parser.Query) string {
	if len(q.Params) == 0 {
		return ""
	}
	var args []string
	if len(q.Params) > 2 {
		for _, p := range q.Params {
			fieldName := utils.ToSnakeCase(p.Name)
			args = append(args, fmt.Sprintf("params.%s", fieldName))
		}
	} else {
		for _, p := range q.Params {
			paramName := utils.ToSnakeCase(p.Name)
			args = append(args, paramName)
		}
	}
	return ", " + strings.Join(args, ", ")
}

// writeMacroExecBody generates exec body using sqlx::query! macro (compile-time validated)
func (g *Generator) writeMacroExecBody(w *strings.Builder, q *parser.Query, sql string, cmd string) {
	args := g.macroArgs(q)
	if cmd == ":execresult" || cmd == "execresult" {
		w.WriteString(fmt.Sprintf("        sqlx::query!(\"%s\"%s)\n", escapeSQLForRust(sql), args))
		w.WriteString("            .execute(&self.pool)\n            .await\n")
	} else {
		w.WriteString(fmt.Sprintf("        sqlx::query!(\"%s\"%s)\n", escapeSQLForRust(sql), args))
		w.WriteString("            .execute(&self.pool)\n            .await?;\n        Ok(())\n")
	}
}

// writeMacroOneBody generates :one body using sqlx::query_as!/query_scalar! macros (compile-time validated)
func (g *Generator) writeMacroOneBody(w *strings.Builder, q *parser.Query, sql string, columns []*parser.QueryColumn) {
	args := g.macroArgs(q)

	if len(columns) == 1 {
		// Single column: use query_scalar! macro
		// sqlx::query_scalar! always returns Option<T> for the scalar value
		w.WriteString(fmt.Sprintf("        sqlx::query_scalar!(\"%s\"%s)\n", escapeSQLForRust(sql), args))
		w.WriteString("            .fetch_one(&self.pool)\n            .await\n")
	} else {
		returnType := g.getMacroOneReturnType(q, columns)
		// Multi column: use query_as! macro with target struct
		w.WriteString(fmt.Sprintf("        sqlx::query_as!(%s, \"%s\"%s)\n", returnType, escapeSQLForRust(sql), args))
		w.WriteString("            .fetch_one(&self.pool)\n            .await\n")
	}
}

// writeMacroManyBody generates :many body using sqlx::query_as!/query_scalar! macros (compile-time validated)
func (g *Generator) writeMacroManyBody(w *strings.Builder, q *parser.Query, sql string, columns []*parser.QueryColumn) {
	args := g.macroArgs(q)

	if len(columns) == 1 {
		// Single column: use query_scalar! macro
		// sqlx::query_scalar! always returns Option<T> for the scalar value
		w.WriteString(fmt.Sprintf("        sqlx::query_scalar!(\"%s\"%s)\n", escapeSQLForRust(sql), args))
		w.WriteString("            .fetch_all(&self.pool)\n            .await\n")
	} else {
		oneType := g.getMacroOneReturnType(q, columns)
		// Multi column: use query_as! macro with target struct
		w.WriteString(fmt.Sprintf("        sqlx::query_as!(%s, \"%s\"%s)\n", oneType, escapeSQLForRust(sql), args))
		w.WriteString("            .fetch_all(&self.pool)\n            .await\n")
	}
}

// getMacroScalarReturnType returns the return type for query_scalar! in macros mode.
// sqlx::query_scalar! always returns Option<T> for the scalar value.
func (g *Generator) getMacroScalarReturnType(col *parser.QueryColumn) string {
	baseType := g.sqlTypeToRust(col.Type, false)
	// query_scalar! always wraps in Option<T>
	return fmt.Sprintf("Option<%s>", baseType)
}

// getMacroOneReturnType returns the struct return type for query_as! in macros mode.
func (g *Generator) getMacroOneReturnType(q *parser.Query, columns []*parser.QueryColumn) string {
	modelType := g.modelTypeForQuery(q, columns)
	if modelType != "" {
		return modelType
	}
	return g.rowTypeName(q)
}

// isActualTableColumn checks if a column name exists as a physical column in the given table.
// Returns false for computed columns, CTE-derived columns, or columns from JOINed tables.
func (g *Generator) isActualTableColumn(colName string, tableName string) bool {
	if g.schema == nil || tableName == "" {
		return false
	}
	for _, table := range g.schema.Tables {
		if strings.EqualFold(table.Name, tableName) {
			for _, col := range table.Columns {
				if strings.EqualFold(col.Name, colName) {
					return true
				}
			}
			return false
		}
	}
	return false
}

func (g *Generator) getOneReturnType(q *parser.Query, columns []*parser.QueryColumn) string {
	if len(columns) == 0 {
		return "()"
	}
	modelType := g.modelTypeForQuery(q, columns)
	if modelType != "" {
		return modelType
	}
	if len(columns) == 1 {
		return g.sqlTypeToRust(columns[0].Type, columns[0].Nullable)
	}
	return g.rowTypeName(q)
}

func (g *Generator) getExecResultType() string {
	switch g.Config.Database.Provider {
	case "mysql":
		return "sqlx::mysql::MySqlQueryResult"
	case "sqlite":
		return "sqlx::sqlite::SqliteQueryResult"
	default:
		return "sqlx::postgres::PgQueryResult"
	}
}

// modelTypeForQuery checks if query columns exactly match a table model.
// Uses the name-based matcher: sqlx maps result columns by name (FromRow /
// query_as!), so a SELECT in a different column order still decodes into the
// model struct.
func (g *Generator) modelTypeForQuery(q *parser.Query, columns []*parser.QueryColumn) string {
	return g.expander.ModelTypeForQueryByName(q, columns)
}

// expandWildcardColumns expands * in queries using schema info
func (g *Generator) expandWildcardColumns(q *parser.Query) []*parser.QueryColumn {
	return g.expander.ExpandWildcardColumns(q)
}

// convertSQL adjusts SQL syntax for the target database driver.
// For sqlx with PostgreSQL, $1/$2 stay as-is. For MySQL/SQLite, convert to ?.
func (g *Generator) convertSQL(sql string) string {
	sql = strings.TrimSpace(sql)
	switch g.Config.Database.Provider {
	case "mysql", "sqlite":
		for i := 99; i >= 1; i-- {
			sql = strings.ReplaceAll(sql, fmt.Sprintf("$%d", i), "?")
		}
	}
	return sql
}

// paramTypeToRust maps param types for function arguments (uses references where appropriate)
func (g *Generator) paramTypeToRust(sqlType string) string {
	rustType := g.sqlTypeToRust(sqlType, false)
	switch rustType {
	case "String":
		return "&str"
	case "Vec<u8>":
		return "&[u8]"
	default:
		if strings.HasPrefix(rustType, "Vec<") {
			return "&" + rustType
		}
		return rustType
	}
}

// collectImports determines which crate imports are needed for a file's queries
func (g *Generator) collectImports(queries []*parser.Query) []string {
	needsChronoNaive := false
	needsChronoUtc := false
	needsUUID := false
	needsDecimal := false
	needsJsonValue := false

	for _, q := range queries {
		for _, col := range g.expandWildcardColumns(q) {
			g.checkTypeImports(col.Type, col.Nullable, &needsChronoNaive, &needsChronoUtc, &needsUUID, &needsDecimal, &needsJsonValue)
		}
		for _, p := range q.Params {
			g.checkTypeImports(p.Type, false, &needsChronoNaive, &needsChronoUtc, &needsUUID, &needsDecimal, &needsJsonValue)
		}
	}

	var imports []string
	if needsChronoNaive || needsChronoUtc {
		parts := []string{}
		if needsChronoNaive {
			parts = append(parts, "NaiveDateTime")
		}
		if needsChronoUtc {
			parts = append(parts, "DateTime", "Utc")
		}
		imports = append(imports, fmt.Sprintf("chrono::{%s}", strings.Join(parts, ", ")))
	}
	if needsUUID {
		imports = append(imports, "uuid::Uuid")
	}
	if needsDecimal {
		imports = append(imports, "rust_decimal::Decimal")
	}
	if needsJsonValue {
		imports = append(imports, "serde_json::Value")
	}
	return imports
}

func (g *Generator) checkTypeImports(sqlType string, nullable bool, needsChronoNaive, needsChronoUtc, needsUUID, needsDecimal, needsJsonValue *bool) {
	rustType := g.sqlTypeToRust(sqlType, nullable)
	if strings.Contains(rustType, "NaiveDateTime") || strings.Contains(rustType, "NaiveDate") || strings.Contains(rustType, "NaiveTime") {
		*needsChronoNaive = true
	}
	if strings.Contains(rustType, "DateTime<Utc>") {
		*needsChronoUtc = true
	}
	if strings.Contains(rustType, "Uuid") {
		*needsUUID = true
	}
	if strings.Contains(rustType, "Decimal") {
		*needsDecimal = true
	}
	if strings.Contains(rustType, "serde_json::Value") {
		*needsJsonValue = true
	}
}

// escapeSQLForRust escapes a SQL string for embedding in Rust string literals
func escapeSQLForRust(sql string) string {
	sql = strings.ReplaceAll(sql, "\\", "\\\\")
	sql = strings.ReplaceAll(sql, "\"", "\\\"")
	sql = strings.ReplaceAll(sql, "\n", " ")
	for strings.Contains(sql, "  ") {
		sql = strings.ReplaceAll(sql, "  ", " ")
	}
	return strings.TrimSpace(sql)
}
