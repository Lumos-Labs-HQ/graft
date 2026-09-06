package seeder

import (
	"context"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/fatih/color"

	"github.com/Lumos-Labs-HQ/flash/internal/config"
	"github.com/Lumos-Labs-HQ/flash/internal/database"
)

// Package-level pre-compiled regexes to avoid recompilation on every parse.
var (
	validIdentifier  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	createTableRegex = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?["']?(\w+)["']?\s*\(([\s\S]*?)\);`)
	seederFKRegex    = regexp.MustCompile(`(?i)FOREIGN\s+KEY\s*\(["']?(\w+)["']?\)\s*REFERENCES\s+["']?(\w+)["']?\s*\(["']?(\w+)["']?\)`)
	seederRefRegex   = regexp.MustCompile(`(?i)REFERENCES\s+["']?(\w+)["']?\s*\(["']?(\w+)["']?\)`)
	createTypeRegex  = regexp.MustCompile(`(?i)CREATE\s+TYPE\s+["']?(\w+)["']?\s+AS\s+ENUM\s*\(([\s\S]*?)\)`)
)

type Seeder struct {
	config      *config.Config
	adapter     database.DatabaseAdapter
	generator   *DataGenerator
	graph       *DependencyGraph
	insertedIDs map[string][]any
	seedConfig  SeedConfig
	enumTypes   map[string][]string // custom enum type name -> values
}

func NewSeeder(cfg *config.Config) (*Seeder, error) {
	adapter := database.NewAdapter(cfg.Database.Provider)

	dbURL, err := cfg.GetDatabaseURL()
	if err != nil {
		return nil, fmt.Errorf("failed to get database URL: %w", err)
	}

	if err := adapter.Connect(context.Background(), dbURL); err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	generator, err := NewDataGenerator()
	if err != nil {
		adapter.Close()
		return nil, fmt.Errorf("failed to create data generator: %w", err)
	}

	return &Seeder{
		config:      cfg,
		adapter:     adapter,
		generator:   generator,
		graph:       NewDependencyGraph(),
		insertedIDs: make(map[string][]any),
		enumTypes:   make(map[string][]string),
	}, nil
}

func isValidIdentifier(name string) bool {
	return validIdentifier.MatchString(name)
}

func (s *Seeder) Close() error {
	return s.adapter.Close()
}

func (s *Seeder) Seed(ctx context.Context, seedConfig SeedConfig) error {
	s.seedConfig = seedConfig
	color.Cyan("🌱 Starting database seeding...")

	tables, err := s.parseSchema()
	if err != nil {
		return fmt.Errorf("failed to parse schema: %w", err)
	}

	if len(tables) == 0 {
		color.Yellow("⚠️  No tables found in schema")
		return nil
	}

	// Apply exclusions
	for _, ex := range seedConfig.Exclude {
		delete(tables, ex)
	}

	// If specific tables were requested, restrict to those tables only.
	// We do NOT auto-include FK parents — the user is responsible for ensuring
	// parent data exists (or using --truncate + seeding parents separately).
	// This matches the documented behaviour: "flash seed users" seeds only users.
	if len(seedConfig.SpecificTables) > 0 {
		requested := make(map[string]bool, len(seedConfig.SpecificTables))
		for _, t := range seedConfig.SpecificTables {
			requested[t] = true
		}
		for name := range tables {
			if !requested[name] {
				delete(tables, name)
			}
		}
	}

	// Validate identifiers
	for tableName, table := range tables {
		if !isValidIdentifier(tableName) {
			return fmt.Errorf("invalid table name: %s", tableName)
		}
		for _, col := range table.Columns {
			if !isValidIdentifier(col.Name) {
				return fmt.Errorf("invalid column name in table %s: %s", tableName, col.Name)
			}
		}
	}

	// Build dependency graph
	for _, table := range tables {
		s.graph.AddTable(table)
	}

	order, err := s.graph.BuildInsertionOrder()
	if err != nil {
		return fmt.Errorf("failed to build insertion order: %w", err)
	}

	// Validate FK references
	if err := s.validateForeignKeys(tables, order); err != nil {
		return fmt.Errorf("foreign key validation failed: %w", err)
	}

	// Determine which tables are referenced by FKs (need ID tracking)
	referencedTables := make(map[string]bool)
	for _, table := range tables {
		for _, col := range table.Columns {
			if col.IsFK && col.FKTable != "" {
				referencedTables[col.FKTable] = true
			}
		}
	}

	color.Green("📊 Found %d tables", len(tables))
	color.Cyan("📋 Insertion order: %s", strings.Join(order, " → "))
	fmt.Println()

	// Dry run: print sample data
	if seedConfig.DryRun {
		return s.dryRun(tables, order)
	}

	// Truncate if requested
	if seedConfig.Truncate {
		if err := s.truncateTables(ctx, order); err != nil {
			if !seedConfig.Force {
				return fmt.Errorf("failed to truncate tables: %w (use --force to continue)", err)
			}
			color.Yellow("⚠️  Truncate failed but continuing with --force: %v", err)
		}
	}

	// Start transaction
	inTransaction := false
	if err := s.beginTransaction(ctx); err != nil {
		color.Yellow("⚠️  Could not start transaction: %v (continuing without transaction)", err)
	} else {
		inTransaction = true
		color.Cyan("🔒 Transaction started")
	}

	var seedErr error
	for _, tableName := range order {
		table := tables[tableName]
		count := seedConfig.Count
		if tableCount, exists := seedConfig.Tables[tableName]; exists {
			count = tableCount
		}

		needsIDs := referencedTables[tableName]
		if err := s.seedTable(ctx, table, count, needsIDs); err != nil {
			if !seedConfig.Force {
				seedErr = fmt.Errorf("failed to seed table %s: %w", tableName, err)
				break
			}
			color.Yellow("⚠️  Failed to seed %s but continuing with --force: %v", tableName, err)
		}
	}

	if inTransaction {
		if seedErr != nil {
			color.Yellow("🔄 Rolling back transaction due to error...")
			if rbErr := s.rollbackTransaction(ctx); rbErr != nil {
				return fmt.Errorf("seed failed and rollback failed: %v (original: %w)", rbErr, seedErr)
			}
			color.Yellow("✅ Transaction rolled back")
			return seedErr
		}

		if err := s.commitTransaction(ctx); err != nil {
			_ = s.rollbackTransaction(ctx)
			return fmt.Errorf("failed to commit transaction: %w", err)
		}
		color.Cyan("🔓 Transaction committed")
	} else if seedErr != nil {
		return seedErr
	}

	color.Green("\n✅ Database seeding completed successfully!")
	return nil
}

func (s *Seeder) dryRun(tables map[string]*TableInfo, order []string) error {
	color.Yellow("🔍 Dry run mode — no data will be inserted")
	fmt.Println()

	for _, tableName := range order {
		table := tables[tableName]
		count := s.seedConfig.Count
		if c, ok := s.seedConfig.Tables[tableName]; ok {
			count = c
		}
		if count > 3 {
			count = 3 // Only show 3 samples in dry run
		}

		color.Cyan("📋 %s (%d sample records):", tableName, count)
		for i := 0; i < count; i++ {
			record := s.generateRecord(table, nil, nil)
			var parts []string
			for _, col := range table.Columns {
				val := record[col.Name]
				if val == nil {
					parts = append(parts, fmt.Sprintf("%s=NULL", col.Name))
				} else {
					parts = append(parts, fmt.Sprintf("%s=%v", col.Name, val))
				}
			}
			fmt.Printf("  Row %d: %s\n", i+1, strings.Join(parts, ", "))
		}
		fmt.Println()
	}
	return nil
}

func (s *Seeder) generateRecord(table *TableInfo, availableIDs map[string][]any, usedFKValues map[string]map[any]bool) map[string]any {
	record := make(map[string]any)

	// Coordinated timestamps: if a table has both created_at and updated_at,
	// ensure updated_at >= created_at.
	var createdAt, updatedAt time.Time
	var createdCol, updatedCol string
	for _, col := range table.Columns {
		cl := strings.ToLower(col.Name)
		if cl == "created_at" || cl == "created_on" || cl == "inserted_at" {
			createdCol = col.Name
		}
		if cl == "updated_at" || cl == "updated_on" || cl == "modified_at" || cl == "modified_on" || cl == "edited_at" {
			updatedCol = col.Name
		}
	}
	if createdCol != "" && updatedCol != "" {
		createdAt = time.Now().AddDate(0, 0, -s.generator.rand.Intn(365))
		updatedAt = createdAt.Add(time.Duration(s.generator.rand.Intn(30*24)+1) * time.Hour)
		if updatedAt.After(time.Now()) {
			updatedAt = time.Now()
		}
	}

	for _, col := range table.Columns {
		if col.IsPK && isAutoIncrementType(col.Type, s.config.Database.Provider) {
			continue
		}

		if col.IsFK {
			key := col.FKTable
			if ids, ok := availableIDs[key]; ok && len(ids) > 0 {
				// For unique FK columns, pick an unused value to avoid duplicate key violations.
				if col.IsUnique {
					if used, tracked := usedFKValues[col.Name]; tracked {
						val := s.pickUnusedID(ids, used)
						if val != nil {
							used[val] = true
							record[col.Name] = val
							continue
						}
						// All IDs exhausted — fall through to nullable/fallback handling
					}
				} else {
					record[col.Name] = ids[s.generator.rand.Intn(len(ids))]
					continue
				}
			}
			if !col.Nullable {
				// NOT NULL FK with no referenced data yet (e.g. first row of self-reference)
				record[col.Name] = s.generator.GenerateForColumn(col.Name, col.Type, col.Nullable, s.enumTypes)
				continue
			}
			record[col.Name] = nil
			continue
		}

		// Coordinated timestamps override
		if col.Name == createdCol {
			record[col.Name] = createdAt
			continue
		}
		if col.Name == updatedCol {
			record[col.Name] = updatedAt
			continue
		}

		record[col.Name] = s.generator.GenerateForColumn(col.Name, col.Type, col.Nullable, s.enumTypes)
	}
	return record
}

// pickUnusedID returns the first ID from ids that hasn't been used yet, or nil if all are used.
func (s *Seeder) pickUnusedID(ids []any, used map[any]bool) any {
	// Shuffle a copy of the indices to avoid always picking in order
	indices := make([]int, len(ids))
	for i := range indices {
		indices[i] = i
	}
	s.generator.rand.Shuffle(len(indices), func(i, j int) { indices[i], indices[j] = indices[j], indices[i] })
	for _, idx := range indices {
		if !used[ids[idx]] {
			return ids[idx]
		}
	}
	return nil
}

func isAutoIncrementType(colType, provider string) bool {
	if provider == "clickhouse" || provider == "scylla" || provider == "scylladb" || provider == "cassandra" {
		return false // ClickHouse/ScyllaDB have no auto-increment
	}
	typeUpper := strings.ToUpper(colType)
	return strings.Contains(typeUpper, "SERIAL") ||
		strings.Contains(typeUpper, "AUTO_INCREMENT") ||
		strings.Contains(typeUpper, "AUTOINCREMENT") ||
		(strings.Contains(typeUpper, "INTEGER") && (provider == "sqlite" || provider == "sqlite3"))
}

func (s *Seeder) seedTable(ctx context.Context, table *TableInfo, count int, needsIDs bool) error {
	color.Cyan("  📝 Seeding %s (%d records)...", table.Name, count)

	batchSize := adaptBatchSize(100, len(table.Columns))
	batch := make([]map[string]any, 0, batchSize)

	// availableIDs accumulates IDs from previous batches + current batch for self-referencing FKs
	availableIDs := make(map[string][]any)
	maps.Copy(availableIDs, s.insertedIDs)

	// usedFKValues tracks which FK values have been used for unique FK columns
	// to prevent duplicate key violations on UNIQUE FK columns.
	usedFKValues := make(map[string]map[any]bool)
	for _, col := range table.Columns {
		if col.IsFK && col.IsUnique {
			usedFKValues[col.Name] = make(map[any]bool)
		}
	}

	var inserted int
	for i := range count {
		record := s.generateRecord(table, availableIDs, usedFKValues)
		batch = append(batch, record)

		if len(batch) >= batchSize || i == count-1 {
			ids, err := s.insertBatch(ctx, table.Name, batch, table.PrimaryKey, needsIDs)
			if err != nil {
				return fmt.Errorf("failed to insert batch: %w", err)
			}

			s.insertedIDs[table.Name] = append(s.insertedIDs[table.Name], ids...)
			availableIDs[table.Name] = s.insertedIDs[table.Name]

			inserted += len(batch)
			batch = batch[:0]
		}
	}

	color.Green("  ✅ %s seeded successfully (%d records)", table.Name, inserted)
	return nil
}

// adaptBatchSize clamps batch size down for very wide tables, never increases it.
func adaptBatchSize(userBatch int, columnCount int) int {
	if userBatch <= 0 {
		userBatch = 100
	}

	estimatedRowBytes := columnCount * 50
	switch {
	case estimatedRowBytes > 5000:
		if userBatch > 50 {
			return 50
		}
	case estimatedRowBytes > 2000:
		if userBatch > 100 {
			return 100
		}
	}
	return userBatch
}

func (s *Seeder) insertBatch(ctx context.Context, tableName string, records []map[string]any, pkColumn string, needsIDs bool) ([]any, error) {
	if len(records) == 0 {
		return nil, nil
	}

	provider := s.config.Database.Provider

	// Fast path: multi-row insert when IDs are not needed.
	if !needsIDs && len(records) > 1 && provider != "sqlite" && provider != "sqlite3" {
		return s.insertMultiRow(ctx, tableName, records)
	}

	// PostgreSQL: use a single multi-row INSERT ... RETURNING for ID tracking.
	if needsIDs && len(records) > 1 && (provider == "postgresql" || provider == "postgres") && pkColumn != "" {
		return s.insertMultiRowReturning(ctx, tableName, records, pkColumn)
	}

	// ID-tracking path for MySQL/SQLite or single records: insert one by one.
	ids := make([]any, 0, len(records))
	for _, record := range records {
		id, err := s.insertRecord(ctx, tableName, record, pkColumn)
		if err != nil {
			return ids, err
		}
		if id != nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (s *Seeder) insertMultiRowReturning(ctx context.Context, tableName string, records []map[string]any, pkColumn string) ([]any, error) {
	if !isValidIdentifier(tableName) {
		return nil, fmt.Errorf("invalid table name: %s", tableName)
	}
	if !isValidIdentifier(pkColumn) {
		return nil, fmt.Errorf("invalid primary key column: %s", pkColumn)
	}

	columns := make([]string, 0, len(records[0]))
	for col := range records[0] {
		if !isValidIdentifier(col) {
			return nil, fmt.Errorf("invalid column name: %s", col)
		}
		columns = append(columns, col)
	}

	allValueStrs := make([]string, 0, len(records))
	for _, record := range records {
		valueStrs := make([]string, 0, len(columns))
		for _, col := range columns {
			valueStrs = append(valueStrs, s.formatValue(record[col]))
		}
		allValueStrs = append(allValueStrs, "("+strings.Join(valueStrs, ", ")+")")
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s RETURNING %s",
		tableName, strings.Join(columns, ", "), strings.Join(allValueStrs, ", "), pkColumn)

	result, err := s.adapter.ExecuteQuery(ctx, query)
	if err != nil {
		return nil, err
	}

	ids := make([]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		if val, ok := row[pkColumn]; ok {
			ids = append(ids, val)
		}
	}
	return ids, nil
}

func (s *Seeder) insertMultiRow(ctx context.Context, tableName string, records []map[string]any) ([]any, error) {
	if !isValidIdentifier(tableName) {
		return nil, fmt.Errorf("invalid table name: %s", tableName)
	}

	columns := make([]string, 0, len(records[0]))
	for col := range records[0] {
		if !isValidIdentifier(col) {
			return nil, fmt.Errorf("invalid column name: %s", col)
		}
		columns = append(columns, col)
	}

	allValueStrs := make([]string, 0, len(records))
	for _, record := range records {
		valueStrs := make([]string, 0, len(columns))
		for _, col := range columns {
			valueStrs = append(valueStrs, s.formatValue(record[col]))
		}
		allValueStrs = append(allValueStrs, "("+strings.Join(valueStrs, ", ")+")")
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s",
		tableName, strings.Join(columns, ", "), strings.Join(allValueStrs, ", "))

	_, err := s.adapter.ExecuteQuery(ctx, query)
	return nil, err
}

func (s *Seeder) insertRecord(ctx context.Context, tableName string, record map[string]any, pkColumn string) (any, error) {
	if !isValidIdentifier(tableName) {
		return nil, fmt.Errorf("invalid table name: %s", tableName)
	}

	columns := make([]string, 0, len(record))
	valueStrs := make([]string, 0, len(record))

	for col, val := range record {
		if !isValidIdentifier(col) {
			return nil, fmt.Errorf("invalid column name: %s", col)
		}
		columns = append(columns, col)
		valueStrs = append(valueStrs, s.formatValue(val))
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		tableName, strings.Join(columns, ", "), strings.Join(valueStrs, ", "))

	provider := s.config.Database.Provider

	// PostgreSQL: RETURNING clause gives us IDs directly.
	if (provider == "postgresql" || provider == "postgres") && pkColumn != "" {
		if !isValidIdentifier(pkColumn) {
			return nil, fmt.Errorf("invalid primary key column: %s", pkColumn)
		}
		query += fmt.Sprintf(" RETURNING %s", pkColumn)

		result, err := s.adapter.ExecuteQuery(ctx, query)
		if err != nil {
			return nil, err
		}
		if result != nil && len(result.Rows) > 0 && pkColumn != "" {
			if val, ok := result.Rows[0][pkColumn]; ok {
				return val, nil
			}
		}
		return nil, nil
	}

	// MySQL / SQLite / others: execute insert, then query last ID.
	_, err := s.adapter.ExecuteQuery(ctx, query)
	if err != nil {
		return nil, err
	}

	switch provider {
	case "sqlite", "sqlite3":
		idResult, err := s.adapter.ExecuteQuery(ctx, "SELECT last_insert_rowid()")
		if err == nil && idResult != nil && len(idResult.Rows) > 0 {
			for _, v := range idResult.Rows[0] {
				return v, nil
			}
		}
	case "mysql":
		idResult, err := s.adapter.ExecuteQuery(ctx, "SELECT LAST_INSERT_ID()")
		if err == nil && idResult != nil && len(idResult.Rows) > 0 {
			for _, v := range idResult.Rows[0] {
				return v, nil
			}
		}
	}

	return nil, nil
}

func (s *Seeder) formatValue(val any) string {
	if val == nil {
		return "NULL"
	}
	switch v := val.(type) {
	case string:
		escaped := strings.ReplaceAll(v, "'", "''")
		escaped = strings.ReplaceAll(escaped, "\\", "\\\\")
		return fmt.Sprintf("'%s'", escaped)
	case int, int32, int64, float32, float64:
		return fmt.Sprintf("%v", v)
	case bool:
		if v {
			return "1"
		}
		return "0"
	case time.Time:
		return fmt.Sprintf("'%s'", v.Format("2006-01-02 15:04:05"))
	case []byte:
		return fmt.Sprintf("'%s'", strings.ReplaceAll(string(v), "'", "''"))
	default:
		escaped := strings.ReplaceAll(fmt.Sprintf("%v", v), "'", "''")
		return fmt.Sprintf("'%s'", escaped)
	}
}

func (s *Seeder) beginTransaction(ctx context.Context) error {
	if s.config.Database.Provider == "clickhouse" || s.config.Database.Provider == "scylla" || s.config.Database.Provider == "scylladb" || s.config.Database.Provider == "cassandra" {
		return fmt.Errorf("%s does not support transactions", s.config.Database.Provider)
	}
	var query string
	switch s.config.Database.Provider {
	case "mysql":
		query = "START TRANSACTION"
	default:
		query = "BEGIN"
	}
	_, err := s.adapter.ExecuteQuery(ctx, query)
	return err
}

func (s *Seeder) commitTransaction(ctx context.Context) error {
	_, err := s.adapter.ExecuteQuery(ctx, "COMMIT")
	return err
}

func (s *Seeder) rollbackTransaction(ctx context.Context) error {
	_, err := s.adapter.ExecuteQuery(ctx, "ROLLBACK")
	return err
}

func (s *Seeder) validateForeignKeys(tables map[string]*TableInfo, order []string) error {
	orderIndex := make(map[string]int)
	for i, name := range order {
		orderIndex[name] = i
	}

	for _, table := range tables {
		for _, col := range table.Columns {
			if col.IsFK && !col.Nullable {
				refIdx, exists := orderIndex[col.FKTable]
				if !exists {
					return fmt.Errorf("table %s has NOT NULL FK column %s referencing non-existent table %s",
						table.Name, col.Name, col.FKTable)
				}
				thisIdx := orderIndex[table.Name]
				// Self-references are allowed (first rows get fallback value)
				if col.FKTable != table.Name && refIdx >= thisIdx {
					return fmt.Errorf("table %s has NOT NULL FK column %s but referenced table %s is seeded later (circular dependency?)",
						table.Name, col.Name, col.FKTable)
				}
			}
		}
	}
	return nil
}

func (s *Seeder) parseSchema() (map[string]*TableInfo, error) {
	schemaFiles, err := s.config.GetSchemaFiles()
	if err != nil {
		return nil, err
	}

	tables := make(map[string]*TableInfo)

	for _, file := range schemaFiles {
		content, err := s.readFile(file)
		if err != nil {
			continue
		}

		// Parse custom enum types (e.g. CREATE TYPE foo AS ENUM ('a', 'b'))
		for _, m := range createTypeRegex.FindAllStringSubmatch(content, -1) {
			if len(m) < 3 {
				continue
			}
			typeName := strings.ToLower(m[1])
			var vals []string
			for _, sm := range enumValueRegex.FindAllStringSubmatch(m[2], -1) {
				if len(sm) >= 2 {
					vals = append(vals, sm[1])
				}
			}
			if len(vals) > 0 {
				s.enumTypes[typeName] = vals
			}
		}

		matches := createTableRegex.FindAllStringSubmatch(content, -1)
		for _, match := range matches {
			if len(match) < 3 {
				continue
			}

			tableName := match[1]
			tableBody := match[2]

			table := s.parseTableDefinition(tableName, tableBody)
			tables[tableName] = table
		}
	}

	return tables, nil
}

// splitColumnDefs splits a CREATE TABLE body on commas that are not inside parentheses.
func splitColumnDefs(body string) []string {
	var defs []string
	var current strings.Builder
	depth := 0
	inString := false
	for i := 0; i < len(body); i++ {
		ch := body[i]
		if inString {
			current.WriteByte(ch)
			if ch == '\'' {
				if i+1 < len(body) && body[i+1] == '\'' {
					i++
					current.WriteByte(body[i])
				} else {
					inString = false
				}
			}
			continue
		}
		switch ch {
		case '\'':
			inString = true
			current.WriteByte(ch)
		case '(':
			depth++
			current.WriteByte(ch)
		case ')':
			depth--
			current.WriteByte(ch)
		case ',':
			if depth == 0 {
				defs = append(defs, current.String())
				current.Reset()
			} else {
				current.WriteByte(ch)
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		defs = append(defs, current.String())
	}
	return defs
}

func (s *Seeder) parseTableDefinition(tableName, body string) *TableInfo {
	table := &TableInfo{
		Name:         tableName,
		Columns:      []ColumnInfo{},
		ForeignKeys:  []ForeignKey{},
		Dependencies: []string{},
	}

	lines := splitColumnDefs(body)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		lineUpper := strings.ToUpper(line)

		// Check for foreign key constraint
		if fkMatch := seederFKRegex.FindStringSubmatch(line); fkMatch != nil {
			fk := ForeignKey{
				Column:    fkMatch[1],
				RefTable:  fkMatch[2],
				RefColumn: fkMatch[3],
			}
			table.ForeignKeys = append(table.ForeignKeys, fk)
			if fkMatch[2] != tableName {
				table.Dependencies = append(table.Dependencies, fkMatch[2])
			}
			continue
		}

		// Skip constraint definitions
		if strings.HasPrefix(lineUpper, "PRIMARY") ||
			strings.HasPrefix(lineUpper, "UNIQUE") ||
			strings.HasPrefix(lineUpper, "CHECK") ||
			strings.HasPrefix(lineUpper, "CONSTRAINT") ||
			strings.HasPrefix(lineUpper, "INDEX") ||
			strings.HasPrefix(lineUpper, "KEY") {
			continue
		}

		// Parse column definition
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		colName := strings.Trim(parts[0], `"'`)
		colType := parts[1]

		col := ColumnInfo{
			Name:     colName,
			Type:     colType,
			Nullable: !strings.Contains(lineUpper, "NOT NULL"),
			IsPK:     strings.Contains(lineUpper, "PRIMARY KEY") || strings.Contains(strings.ToUpper(colType), "SERIAL"),
			IsUnique: strings.Contains(lineUpper, "UNIQUE"),
		}

		// Check for inline REFERENCES
		if strings.Contains(lineUpper, "REFERENCES") {
			if refMatch := seederRefRegex.FindStringSubmatch(line); refMatch != nil {
				col.IsFK = true
				col.FKTable = refMatch[1]
				col.FKColumn = refMatch[2]
				if refMatch[1] != tableName {
					table.Dependencies = append(table.Dependencies, refMatch[1])
				}
			}
		}

		if col.IsPK {
			table.PrimaryKey = colName
		}

		table.Columns = append(table.Columns, col)
	}

	// Mark columns that are foreign keys
	for _, fk := range table.ForeignKeys {
		for i := range table.Columns {
			if table.Columns[i].Name == fk.Column {
				table.Columns[i].IsFK = true
				table.Columns[i].FKTable = fk.RefTable
				table.Columns[i].FKColumn = fk.RefColumn
				break
			}
		}
	}

	return table
}

func (s *Seeder) truncateTables(ctx context.Context, order []string) error {
	color.Yellow("🗑️  Truncating tables...")

	errors := make([]string, 0, 4)

	// Reverse order for truncation (to respect FK constraints)
	for _, tableName := range slices.Backward(order) {

		if !isValidIdentifier(tableName) {
			errors = append(errors, fmt.Sprintf("invalid table name: %s", tableName))
			continue
		}

		var query string
		var err error

		switch s.config.Database.Provider {
		case "postgresql", "postgres":
			query = fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE", tableName)
			_, err = s.adapter.ExecuteQuery(ctx, query)
		case "mysql":
			query = fmt.Sprintf("TRUNCATE TABLE %s", tableName)
			_, err = s.adapter.ExecuteQuery(ctx, query)
		case "sqlite", "sqlite3":
			query = fmt.Sprintf("DELETE FROM %s", tableName)
			_, err = s.adapter.ExecuteQuery(ctx, query)
			if err == nil {
				resetQuery := fmt.Sprintf("DELETE FROM sqlite_sequence WHERE name='%s'", tableName)
				_, _ = s.adapter.ExecuteQuery(ctx, resetQuery)
			}
		case "clickhouse":
			query = fmt.Sprintf("ALTER TABLE `%s` DELETE WHERE 1=1", tableName)
			_, err = s.adapter.ExecuteQuery(ctx, query)
		case "scylla", "scylladb", "cassandra":
			query = fmt.Sprintf("TRUNCATE TABLE \"%s\"", tableName)
			_, err = s.adapter.ExecuteQuery(ctx, query)
		default:
			query = fmt.Sprintf("DELETE FROM %s", tableName)
			_, err = s.adapter.ExecuteQuery(ctx, query)
		}

		if err != nil {
			errMsg := fmt.Sprintf("failed to truncate %s: %v", tableName, err)
			errors = append(errors, errMsg)
			color.Yellow("  ⚠️  %s", errMsg)
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("truncate errors: %s", strings.Join(errors, "; "))
	}

	color.Green("✅ Tables truncated")
	fmt.Println()
	return nil
}

func (s *Seeder) readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
