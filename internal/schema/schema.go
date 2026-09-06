package schema

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/database"
	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

type foreignKeyConstraint struct {
	ColumnName, ReferencedTable, ReferencedColumn, OnDeleteAction, OnUpdateAction string
}

type SchemaManager struct {
	adapter database.DatabaseAdapter
}

func NewSchemaManager(adapter database.DatabaseAdapter) *SchemaManager {
	return &SchemaManager{adapter: adapter}
}

// ParseSchemaFile parses a single schema file (legacy support)
func (sm *SchemaManager) ParseSchemaFile(schemaPath string) ([]types.SchemaTable, error) {
	content, err := os.ReadFile(schemaPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read schema file: %w", err)
	}
	tables, _, _ := sm.parseSchemaContent(string(content))
	return tables, nil
}

// ParseSchemaDir parses all .sql files in a directory
func (sm *SchemaManager) ParseSchemaDir(schemaDir string) ([]types.SchemaTable, []types.SchemaEnum, []types.SchemaIndex, error) {
	tables, enums, indexes, _, err := sm.parseSchemaDirAll(schemaDir)
	return tables, enums, indexes, err
}

func (sm *SchemaManager) parseSchemaDirAll(schemaDir string) ([]types.SchemaTable, []types.SchemaEnum, []types.SchemaIndex, []types.SchemaKeyspace, error) {
	tables, enums, indexes, keyspaces, _, _, err := sm.parseSchemaDirAllV2(schemaDir)
	return tables, enums, indexes, keyspaces, err
}

func (sm *SchemaManager) parseSchemaDirAllV2(schemaDir string) ([]types.SchemaTable, []types.SchemaEnum, []types.SchemaIndex, []types.SchemaKeyspace, []types.SchemaUDT, []string, error) {
	entries, err := os.ReadDir(schemaDir)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("failed to read schema directory: %w", err)
	}

	var allTables []types.SchemaTable
	var allEnums []types.SchemaEnum
	var allIndexes []types.SchemaIndex
	var allKeyspaces []types.SchemaKeyspace
	var allUDTs []types.SchemaUDT
	var allRaw []string
	tableMap := make(map[string]*types.SchemaTable)

	var sqlFiles []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") || strings.HasSuffix(entry.Name(), ".cql") {
			sqlFiles = append(sqlFiles, entry.Name())
		}
	}
	slices.Sort(sqlFiles)

	for _, fileName := range sqlFiles {
		filePath := fmt.Sprintf("%s/%s", schemaDir, fileName)
		content, err := os.ReadFile(filePath)
		if err != nil {
			return nil, nil, nil, nil, nil, nil, fmt.Errorf("failed to read schema file %s: %w", filePath, err)
		}

		tables, enums, indexes, keyspaces, udts, raw, err := sm.parseSchemaContentAllV2(string(content))
		if err != nil {
			return nil, nil, nil, nil, nil, nil, fmt.Errorf("failed to parse schema file %s: %w", filePath, err)
		}

		for _, table := range tables {
			if existing, ok := tableMap[table.Name]; ok {
				existingCols := make(map[string]bool)
				for _, col := range existing.Columns {
					existingCols[col.Name] = true
				}
				for _, col := range table.Columns {
					if !existingCols[col.Name] {
						existing.Columns = append(existing.Columns, col)
					}
				}
				existing.Indexes = append(existing.Indexes, table.Indexes...)
			} else {
				tableCopy := table
				tableMap[table.Name] = &tableCopy
			}
		}

		allEnums = append(allEnums, enums...)
		allIndexes = append(allIndexes, indexes...)
		allKeyspaces = append(allKeyspaces, keyspaces...)
		allUDTs = append(allUDTs, udts...)
		allRaw = append(allRaw, raw...)
	}

	for _, table := range tableMap {
		allTables = append(allTables, *table)
	}

	allTables, err = sm.sortTablesByDependencies(allTables)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}

	return allTables, allEnums, allIndexes, allKeyspaces, allUDTs, allRaw, nil
}

// sortTablesByDependencies sorts tables so that referenced tables come before referencing tables.
// Also validates that all referenced tables exist.
// Providers without FK support (ScyllaDB, ClickHouse) skip validation — REFERENCES in schemas
// are treated as documentation hints, not enforceable constraints.
func (sm *SchemaManager) sortTablesByDependencies(tables []types.SchemaTable) ([]types.SchemaTable, error) {
	tableMap := make(map[string]*types.SchemaTable)
	for i := range tables {
		tableMap[tables[i].Name] = &tables[i]
	}

	skipFKs := false
	if sm.adapter != nil {
		provider := sm.adapter.ProviderName()
		skipFKs = provider == "scylla" || provider == "clickhouse"
	}

	// Build dependency graph and validate references
	// dependencies[A] = [B, C] means table A depends on tables B and C (A has FK to B and C)
	dependencies := make(map[string][]string)
	for _, table := range tables {
		var deps []string
		for _, col := range table.Columns {
			if col.ForeignKeyTable != "" {
				if skipFKs {
					// ScyllaDB/ClickHouse don't support FKs — REFERENCES is documentation-only.
					// Ignore for dependency ordering and skip existence validation.
					continue
				}
				if _, exists := tableMap[col.ForeignKeyTable]; !exists {
					return nil, fmt.Errorf("table '%s' references non-existent table '%s' (column '%s' has REFERENCES %s(%s))",
						table.Name, col.ForeignKeyTable, col.Name, col.ForeignKeyTable, col.ForeignKeyColumn)
				}
				if col.ForeignKeyTable != table.Name {
					deps = append(deps, col.ForeignKeyTable)
				}
			}
		}
		dependencies[table.Name] = deps
	}

	// Topological sort using Kahn's algorithm
	// dependencies[A] = [B, C] means A depends on B and C (A has FK to B and C)
	// We want B and C created BEFORE A, so A's in-degree = number of dependencies
	var sorted []types.SchemaTable
	inDegree := make(map[string]int)

	// Build reverse adjacency list: dependents[dep] = []tables that depend on dep
	// This avoids O(T×D) inner scan during the sort loop
	dependents := make(map[string][]string)

	// Ensure every table has an entry so tables with no FKs are still processed.
	for _, table := range tables {
		inDegree[table.Name] = 0
	}

	// Calculate in-degree and build reverse adjacency list
	for tableName, deps := range dependencies {
		inDegree[tableName] = len(deps)
		for _, dep := range deps {
			dependents[dep] = append(dependents[dep], tableName)
		}
	}

	// Find all tables with no dependencies (in-degree = 0)
	var queue []string
	for tableName, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, tableName)
		}
	}

	// Process tables (sort queue only once for determinism)
	slices.Sort(queue)

	for len(queue) > 0 {
		tableName := queue[0]
		queue = queue[1:]

		if table, exists := tableMap[tableName]; exists {
			sorted = append(sorted, *table)
		}

		// Reduce in-degree for all tables that depend on this one — O(1) lookup via reverse adjacency
		for _, depTableName := range dependents[tableName] {
			inDegree[depTableName]--
			if inDegree[depTableName] == 0 {
				// Insert in sorted position to maintain determinism
				insertPos := 0
				for insertPos < len(queue) && queue[insertPos] < depTableName {
					insertPos++
				}
				queue = append(queue[:insertPos], append([]string{depTableName}, queue[insertPos:]...)...)
			}
		}
	}

	// Check for circular dependencies
	if len(sorted) != len(tables) {
		// Find tables involved in circular dependency
		var circular []string
		for tableName, degree := range inDegree {
			if degree > 0 {
				circular = append(circular, tableName)
			}
		}
		return nil, fmt.Errorf("circular foreign key dependency detected among tables: %v", circular)
	}

	return sorted, nil
}

// ParseSchemaPath parses schema from either a file or directory
func (sm *SchemaManager) ParseSchemaPath(schemaPath string) ([]types.SchemaTable, []types.SchemaEnum, []types.SchemaIndex, error) {
	tables, enums, indexes, _, err := sm.ParseSchemaPathAll(schemaPath)
	return tables, enums, indexes, err
}

func (sm *SchemaManager) ParseSchemaPathAll(schemaPath string) ([]types.SchemaTable, []types.SchemaEnum, []types.SchemaIndex, []types.SchemaKeyspace, error) {
	tables, enums, indexes, keyspaces, _, _, err := sm.ParseSchemaPathAllV2(schemaPath)
	return tables, enums, indexes, keyspaces, err
}

func (sm *SchemaManager) ParseSchemaPathAllV2(schemaPath string) ([]types.SchemaTable, []types.SchemaEnum, []types.SchemaIndex, []types.SchemaKeyspace, []types.SchemaUDT, []string, error) {
	info, err := os.Stat(schemaPath)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("failed to stat schema path: %w", err)
	}

	if info.IsDir() {
		return sm.parseSchemaDirAllV2(schemaPath)
	}

	content, err := os.ReadFile(schemaPath)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("failed to read schema file: %w", err)
	}
	var allTables []types.SchemaTable
	var allEnums []types.SchemaEnum
	var allIndexes []types.SchemaIndex
	var allKeyspaces []types.SchemaKeyspace
	var allUDTs []types.SchemaUDT
	var allRaw []string

	allTables, allEnums, allIndexes, allKeyspaces, allUDTs, allRaw, err = sm.parseSchemaContentAllV2(string(content))
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	allTables, err = sm.sortTablesByDependencies(allTables)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	return allTables, allEnums, allIndexes, allKeyspaces, allUDTs, allRaw, nil
}

func (sm *SchemaManager) ParseSchemaFileWithEnumsAndIndexes(schemaPath string) ([]types.SchemaTable, []types.SchemaEnum, []types.SchemaIndex, error) {
	content, err := os.ReadFile(schemaPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to read schema file: %w", err)
	}
	tables, enums, indexes, _, _, _, parseErr := sm.parseSchemaContentAllV2(string(content))
	if parseErr != nil {
		return nil, nil, nil, parseErr
	}
	tables, err = sm.sortTablesByDependencies(tables)
	if err != nil {
		return nil, nil, nil, err
	}
	return tables, enums, indexes, nil
}

func (sm *SchemaManager) ParseSchemaFileWithEnums(schemaPath string) ([]types.SchemaTable, []types.SchemaEnum, error) {
	content, err := os.ReadFile(schemaPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read schema file: %w", err)
	}
	return sm.parseSchemaContent(string(content))
}

func (sm *SchemaManager) parseSchemaContent(content string) ([]types.SchemaTable, []types.SchemaEnum, error) {
	tables, enums, _, err := sm.parseSchemaContentWithIndexes(content)
	return tables, enums, err
}

func (sm *SchemaManager) parseSchemaContentWithIndexes(content string) ([]types.SchemaTable, []types.SchemaEnum, []types.SchemaIndex, error) {
	tables, enums, indexes, _, _, _, err := sm.parseSchemaContentAllV2(content)
	return tables, enums, indexes, err
}

func (sm *SchemaManager) parseSchemaContentAllV2(content string) ([]types.SchemaTable, []types.SchemaEnum, []types.SchemaIndex, []types.SchemaKeyspace, []types.SchemaUDT, []string, error) {
	var tables []types.SchemaTable
	var enums []types.SchemaEnum
	var indexes []types.SchemaIndex
	var keyspaces []types.SchemaKeyspace
	var udts []types.SchemaUDT

	// Extract raw statements (functions, triggers, DOMAIN, PARTITION OF, composite types)
	// from original content before cleanSQL destroys dollar-quote bodies and comments.
	rawStatements := extractRawStatements(content)

	statements := sm.splitStatements(sm.cleanSQL(content))
	tableMap := make(map[string]*types.SchemaTable)

	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}

		upper := strings.ToUpper(stmt)
		// Skip statements that were already captured as raw
		if strings.HasPrefix(upper, "CREATE DOMAIN") ||
			strings.HasPrefix(upper, "CREATE OR REPLACE FUNCTION") ||
			strings.HasPrefix(upper, "CREATE FUNCTION") ||
			strings.HasPrefix(upper, "CREATE OR REPLACE TRIGGER") ||
			strings.HasPrefix(upper, "CREATE TRIGGER") ||
			(strings.HasPrefix(upper, "CREATE TABLE") && strings.Contains(upper, "PARTITION OF")) {
			continue
		}

		if sm.isCreateUDTStatement(stmt) {
			// PostgreSQL composite types (CREATE TYPE name AS (...)) look like CQL UDTs.
			// Treat them as raw passthrough — they've already been captured above if they
			// contain dollar quotes; plain composite types fall through here.
			// We store them as UDTs for schema tracking (diff/snapshot) only.
			if udt, err := sm.parseCreateUDTStatement(stmt); err == nil {
				udts = append(udts, udt)
			}
		} else if sm.isCreateTypeStatement(stmt) {
			if enum, err := sm.parseCreateTypeStatement(stmt); err == nil {
				enums = append(enums, enum)
			}
		} else if sm.isCreateViewStatement(stmt) {
			// Views and MATERIALIZED VIEWs are stored as metadata tables for diff tracking.
			// The marker column distinguishes plain views from materialized views.
			if viewName, viewSQL, err := sm.parseCreateViewStatement(stmt); err == nil {
				marker := "/* VIEW */"
				if strings.Contains(strings.ToUpper(viewSQL), "MATERIALIZED VIEW") {
					marker = "/* MATERIALIZED VIEW */"
				}
				tables = append(tables, types.SchemaTable{
					Name:    viewName,
					Columns: []types.SchemaColumn{{Name: marker, Type: viewSQL, IsPrimary: true}},
				})
			}
		} else if sm.isCreateTableStatement(stmt) {
			upper := strings.ToUpper(stmt)
			if strings.Contains(upper, "PARTITION OF") {
				// Handled as raw statement — skip table parsing
			} else if table, err := sm.parseCreateTableStatement(stmt); err == nil {
				tables = append(tables, table)
				tableMap[table.Name] = &tables[len(tables)-1]
			}
		} else if sm.isCreateIndexStatement(stmt) {
			if index, err := sm.parseCreateIndexStatement(stmt); err == nil {
				indexes = append(indexes, index)
				if table, ok := tableMap[index.Table]; ok {
					table.Indexes = append(table.Indexes, index)
				}
			}
		} else if sm.isCreateKeyspaceStatement(stmt) {
			if ks, err := sm.parseCreateKeyspaceStatement(stmt); err == nil {
				keyspaces = append(keyspaces, ks)
			}
		}
	}
	return tables, enums, indexes, keyspaces, udts, rawStatements, nil
}

// extractRawStatements splits the original SQL (preserving dollar-quote bodies)
// and returns statements that should be passed through verbatim into migrations:
// DOMAIN types, composite types (CREATE TYPE ... AS (...)), PARTITION OF tables,
// functions (CREATE [OR REPLACE] FUNCTION), and triggers.
func extractRawStatements(content string) []string {
	stmts := splitStatementsDollarAware(content)
	var raw []string
	for _, s := range stmts {
		upper := strings.ToUpper(strings.TrimSpace(s))
		if strings.HasPrefix(upper, "CREATE DOMAIN") ||
			strings.HasPrefix(upper, "CREATE EXTENSION") ||
			strings.HasPrefix(upper, "CREATE OR REPLACE FUNCTION") ||
			strings.HasPrefix(upper, "CREATE FUNCTION") ||
			strings.HasPrefix(upper, "CREATE OR REPLACE TRIGGER") ||
			strings.HasPrefix(upper, "CREATE TRIGGER") ||
			(strings.HasPrefix(upper, "CREATE TABLE") && strings.Contains(upper, "PARTITION OF")) ||
			isCompositeType(upper) {
			t := strings.TrimSpace(s)
			if !strings.HasSuffix(t, ";") {
				t += ";"
			}
			raw = append(raw, t)
		}
	}
	return raw
}

// isCompositeType returns true for PostgreSQL composite type declarations:
// CREATE TYPE name AS ( field type, ... )
// Distinguishes from ENUM types (CREATE TYPE name AS ENUM) and CQL UDTs.
func isCompositeType(upper string) bool {
	if !strings.HasPrefix(upper, "CREATE TYPE") {
		return false
	}
	// Must have AS ( but NOT AS ENUM
	_, after0, ok := strings.Cut(upper, " AS ")
	if !ok {
		return false
	}
	after := strings.TrimSpace(after0)
	return strings.HasPrefix(after, "(") && !strings.HasPrefix(after, "ENUM")
}

// splitStatementsDollarAware splits SQL on top-level semicolons, treating
// $$ ... $$ and $tag$ ... $tag$ blocks as opaque (no splitting inside them).
// Also skips -- line comments and /* block comments */ at the top level.
func splitStatementsDollarAware(sql string) []string {
	var result []string
	var cur strings.Builder
	i := 0
	n := len(sql)

	for i < n {
		// -- line comment
		if i+1 < n && sql[i] == '-' && sql[i+1] == '-' {
			for i < n && sql[i] != '\n' {
				i++
			}
			continue
		}
		// /* block comment */
		if i+1 < n && sql[i] == '/' && sql[i+1] == '*' {
			i += 2
			for i+1 < n && !(sql[i] == '*' && sql[i+1] == '/') {
				i++
			}
			i += 2
			continue
		}
		// Dollar-quote: $tag$ or $$
		if sql[i] == '$' {
			// find closing $
			j := i + 1
			for j < n && sql[j] != '$' && sql[j] != '\n' && sql[j] != ' ' {
				j++
			}
			if j < n && sql[j] == '$' {
				tag := sql[i : j+1] // e.g. "$$" or "$func$"
				cur.WriteString(tag)
				i = j + 1
				// scan until closing tag
				for i < n {
					if strings.HasPrefix(sql[i:], tag) {
						cur.WriteString(tag)
						i += len(tag)
						break
					}
					cur.WriteByte(sql[i])
					i++
				}
				continue
			}
		}
		// Single-quoted string
		if sql[i] == '\'' {
			cur.WriteByte(sql[i])
			i++
			for i < n {
				cur.WriteByte(sql[i])
				if sql[i] == '\'' {
					if i+1 < n && sql[i+1] == '\'' {
						i++
						cur.WriteByte(sql[i])
					} else {
						i++
						break
					}
				}
				i++
			}
			continue
		}
		// Statement terminator — but only if not inside BEGIN...END block
		if sql[i] == ';' {
			bufUpper := strings.ToUpper(cur.String())
			trimmed := strings.TrimSpace(bufUpper)
			beginCount := strings.Count(bufUpper, " BEGIN") + strings.Count(bufUpper, "\nBEGIN") + strings.Count(bufUpper, "\tBEGIN")
			if strings.HasPrefix(trimmed, "BEGIN") {
				beginCount++
			}
			endCount := strings.Count(bufUpper, "\nEND") + strings.Count(bufUpper, " END") + strings.Count(bufUpper, "\tEND")
			if strings.HasSuffix(trimmed, "END") || strings.HasSuffix(trimmed, "END;") {
				endCount++
			}
			if beginCount > endCount {
				cur.WriteByte(sql[i])
				i++
				continue
			}
			s := strings.TrimSpace(cur.String())
			if s != "" {
				result = append(result, s)
			}
			cur.Reset()
			i++
			continue
		}
		cur.WriteByte(sql[i])
		i++
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		result = append(result, s)
	}
	return result
}

func (sm *SchemaManager) GenerateSchemaDiff(ctx context.Context, targetSchemaPath string, snapshotPath string) (*types.SchemaDiff, error) {
	var currentTables []types.SchemaTable
	var currentEnums []types.SchemaEnum

	snap, err := LoadSchemaSnapshot(snapshotPath)
	if err != nil {
		fmt.Printf("⚠️  Schema snapshot corrupted (%v). Falling back to live database.\n", err)
	}

	if snap != nil && err == nil {
		currentTables = snap.Tables
		currentEnums = snap.Enums
		for i, table := range currentTables {
			for _, idx := range snap.Indexes {
				if strings.EqualFold(idx.Table, table.Name) {
					dup := false
					for _, existing := range table.Indexes {
						if existing.Name == idx.Name {
							dup = true
							break
						}
					}
					if !dup {
						currentTables[i].Indexes = append(currentTables[i].Indexes, idx)
					}
				}
			}
		}
	} else {
		// No snapshot — fall back to live database introspection.
		// If the DB has tables, use them as the baseline so we don't
		// regenerate CREATE TABLE for already-applied tables.
		currentTables, err = sm.adapter.GetCurrentSchema(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get current schema: %w", err)
		}

		// Filter system tables for ScyllaDB/Cassandra (internal keyspace tables)
		filtered := make([]types.SchemaTable, 0, len(currentTables))
		for _, t := range currentTables {
			name := strings.ToLower(t.Name)
			if strings.HasPrefix(name, "system.") || strings.HasPrefix(name, "system_") {
				continue
			}
			if name == "indexinfo" || name == "batchlog" || name == "batchlog_v2" {
				continue
			}
			filtered = append(filtered, t)
		}
		currentTables = filtered

		currentEnums, err = sm.adapter.GetCurrentEnums(ctx)
		if err != nil {
			currentEnums = []types.SchemaEnum{}
		}

		// DO NOT save live-DB state as snapshot here.
		// The snapshot is saved AFTER migration generation (in GenerateMigration)
		// from the parsed schema files, not the live DB. This keeps the snapshot
		// as the authoritative "last schema we generated a migration for."
	}

	// Parse target schema including keyspaces and UDTs
	targetTables, targetEnums, targetIndexes, targetKeyspaces, targetUDTs, targetRaw, err := sm.ParseSchemaPathAllV2(targetSchemaPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse target schema: %w", err)
	}

	diff := sm.compareSchemas(currentTables, targetTables, currentEnums, targetEnums, targetIndexes)
	sm.compareKeyspaces(snap, targetKeyspaces, diff)
	sm.compareUDTs(snap, targetUDTs, diff)
	sm.compareRawStatements(snap, targetRaw, diff)

	return diff, nil
}

func (sm *SchemaManager) compareKeyspaces(snap *SchemaSnapshot, target []types.SchemaKeyspace, diff *types.SchemaDiff) {
	currentMap := make(map[string]types.SchemaKeyspace)
	if snap != nil {
		for _, ks := range snap.Keyspaces {
			currentMap[ks.Name] = ks
		}
	}

	for _, ks := range target {
		if _, exists := currentMap[ks.Name]; !exists {
			diff.NewKeyspaces = append(diff.NewKeyspaces, ks)
		}
	}
	// Note: DroppedKeyspaces not handled automatically — user must manually add DROP KEYSPACE
}

func (sm *SchemaManager) compareUDTs(snap *SchemaSnapshot, target []types.SchemaUDT, diff *types.SchemaDiff) {
	currentMap := make(map[string]types.SchemaUDT)
	if snap != nil {
		for _, u := range snap.UDTs {
			currentMap[u.Name] = u
		}
	}

	for _, u := range target {
		if _, exists := currentMap[u.Name]; !exists {
			diff.NewUDTs = append(diff.NewUDTs, u)
		}
	}
}

func (sm *SchemaManager) GenerateSchemaSQL(tables []types.SchemaTable) string {
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })

	var parts []string
	for _, table := range tables {
		parts = append(parts, sm.adapter.GenerateCreateTableSQL(table))
		for _, index := range table.Indexes {
			parts = append(parts, sm.adapter.GenerateAddIndexSQL(index))
		}
	}
	return strings.Join(parts, "\n\n")
}

func (sm *SchemaManager) GenerateMigrationSQL(diff *types.SchemaDiff) string {
	var parts []string

	// Drop enums that are no longer needed (must be done before dropping tables)
	for _, enumName := range diff.DroppedEnums {
		parts = append(parts, fmt.Sprintf("DROP TYPE IF EXISTS \"%s\";", enumName))
	}

	for _, tableName := range diff.DroppedTables {
		parts = append(parts, fmt.Sprintf("DROP TABLE IF EXISTS \"%s\";", tableName))
	}

	// Create new enums (must be done before creating tables that use them)
	for _, enum := range diff.NewEnums {
		values := make([]string, len(enum.Values))
		for i, v := range enum.Values {
			values[i] = fmt.Sprintf("'%s'", v)
		}
		parts = append(parts, fmt.Sprintf("CREATE TYPE \"%s\" AS ENUM (%s);", enum.Name, strings.Join(values, ", ")))
	}

	for _, table := range diff.NewTables {
		parts = append(parts, sm.adapter.GenerateCreateTableSQL(table))
		for _, index := range table.Indexes {
			parts = append(parts, sm.adapter.GenerateAddIndexSQL(index))
		}
	}

	for _, tableDiff := range diff.ModifiedTables {
		for _, column := range tableDiff.NewColumns {
			parts = append(parts, sm.adapter.GenerateAddColumnSQL(tableDiff.Name, column))
		}
		for _, column := range tableDiff.DroppedColumns {
			parts = append(parts, sm.adapter.GenerateDropColumnSQL(tableDiff.Name, column.Name))
		}
	}

	for _, index := range diff.DroppedIndexes {
		parts = append(parts, sm.adapter.GenerateDropIndexSQL(index))
	}
	for _, index := range diff.NewIndexes {
		parts = append(parts, sm.adapter.GenerateAddIndexSQL(index))
	}

	return strings.Join(parts, "\n\n")
}
