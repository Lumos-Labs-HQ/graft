package migrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Lumos-Labs-HQ/flash/internal/schema"
	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

// GenerateMigration creates a new migration file - simplified
func (m *Migrator) GenerateMigration(ctx context.Context, name string, schemaPath string) error {
	if schemaPath == "" {
		schemaPath = m.schemaPath
	}

	// Use the local schema snapshot for diffing so we can generate migrations
	// even when previous ones haven't been applied yet.
	snapshotPath := schema.SnapshotPath(m.migrationsDir)

	diff, err := m.schemaManager.GenerateSchemaDiff(ctx, schemaPath, snapshotPath)
	if err != nil {
		return fmt.Errorf("failed to generate schema diff: %w", err)
	}

	filename := m.fileUtils.GenerateMigrationFilename(name)
	filepath := filepath.Join(m.migrationsDir, filename)

	var sqlContent string
	// Check for index changes and keyspace changes too, not just tables and enums.
	if len(diff.NewTables) == 0 && len(diff.DroppedTables) == 0 && len(diff.ModifiedTables) == 0 &&
		len(diff.NewEnums) == 0 && len(diff.DroppedEnums) == 0 && len(diff.ModifiedEnums) == 0 &&
		len(diff.NewIndexes) == 0 && len(diff.DroppedIndexes) == 0 &&
		len(diff.NewKeyspaces) == 0 && len(diff.DroppedKeyspaces) == 0 &&
		len(diff.NewUDTs) == 0 && len(diff.DroppedUDTs) == 0 &&
		len(diff.NewRawStatements) == 0 {
		fmt.Println("No changes detected in schema, creating empty migration template")
		sqlContent = m.generateEmptyMigrationTemplate(name)
	} else {
		sqlContent, _ = m.generateSQLFromDiff(diff, name)
	}

	if err := os.WriteFile(filepath, []byte(sqlContent), 0644); err != nil {
		return fmt.Errorf("failed to write migration file: %w", err)
	}

	// After generating the migration, update the snapshot so the next
	// generation diffs against this new schema state.
	targetTables, targetEnums, targetIndexes, targetKeyspaces, targetUDTs, targetRaw, err := m.schemaManager.ParseSchemaPathAllV2(schemaPath)
	if err != nil {
		return fmt.Errorf("failed to parse target schema for snapshot: %w", err)
	}

	if err := schema.SaveSchemaSnapshotFullV3(snapshotPath, targetTables, targetEnums, targetIndexes, targetKeyspaces, targetUDTs, targetRaw); err != nil {
		return fmt.Errorf("failed to save schema snapshot: %w", err)
	}

	fmt.Printf("Generated migration: %s\n", filename)
	return nil
}

// generateSQLFromDiff creates SQL from schema differences with both UP and DOWN.
// It returns the formatted migration file and a bool indicating whether any
// executable (non-comment) SQL statements were generated.
func (m *Migrator) generateSQLFromDiff(diff *types.SchemaDiff, name string) (string, bool) {
	var upStatements []string
	var downStatements []string
	hasExecutableSQL := false

	dropTableSQL := func(tableName string) string {
		switch m.provider {
		case "scylla", "scylladb", "cassandra":
			// ScyllaDB uses keyspace-qualified names (ks.table). If the name
			// already has a dot, quote each part separately.
			if before, after, ok := strings.Cut(tableName, "."); ok {
				ks := strings.TrimSpace(before)
				tbl := strings.TrimSpace(after)
				return fmt.Sprintf(`DROP TABLE IF EXISTS "%s"."%s";`, strings.Trim(ks, `"`), strings.Trim(tbl, `"`))
			}
			return fmt.Sprintf(`DROP TABLE IF EXISTS "%s"."%s";`, m.adapter.QuoteIdentifier(tableName), tableName)
		default:
			switch m.provider {
			case "sqlite", "sqlite3":
				return fmt.Sprintf("DROP TABLE IF EXISTS \"%s\";", tableName)
			case "mysql":
				return fmt.Sprintf("DROP TABLE IF EXISTS `%s`;", tableName)
			case "clickhouse":
				return fmt.Sprintf("DROP TABLE IF EXISTS `%s`;", tableName)
			default:
				return fmt.Sprintf("DROP TABLE IF EXISTS \"%s\" CASCADE;", tableName)
			}
		}
	}

	dropIndexSQL := func(index types.SchemaIndex) string {
		switch m.provider {
		case "scylla", "scylladb", "cassandra":
			return m.adapter.GenerateDropIndexSQL(index)
		default:
			return fmt.Sprintf("DROP INDEX IF EXISTS \"%s\";", index.Name)
		}
	}

	for _, enum := range diff.NewEnums {
		values := make([]string, len(enum.Values))
		for i, v := range enum.Values {
			// Escape single quotes in enum values for SQL safety
			escapedValue := strings.ReplaceAll(v, "'", "''")
			values[i] = fmt.Sprintf("'%s'", escapedValue)
		}
		// Escape the enum name for both single-quoted string and double-quoted identifier
		escapedNameSingle := strings.ReplaceAll(enum.Name, "'", "''")
		escapedNameDouble := strings.ReplaceAll(enum.Name, "\"", "\"\"")

		// PostgreSQL-specific enum creation with existence guard
		if m.provider == "postgresql" || m.provider == "postgres" {
			enumSQL := fmt.Sprintf(`DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = '%s') THEN
        CREATE TYPE "%s" AS ENUM (%s);
    END IF;
END $$;`, escapedNameSingle, escapedNameDouble, strings.Join(values, ", "))
			upStatements = append(upStatements, enumSQL)
			hasExecutableSQL = true
			// DOWN: Drop enum (escape double quotes for identifier)
			downStatements = append([]string{fmt.Sprintf("DROP TYPE IF EXISTS \"%s\";", escapedNameDouble)}, downStatements...)
		} else if m.provider == "mysql" {
			// MySQL enums are inline on columns; standalone enum changes should not generate SQL here
			// because they are handled as column type changes in ModifiedTables.
			continue
		} else if m.provider == "sqlite" || m.provider == "sqlite3" {
			// SQLite does not support user-defined types; skip enum SQL generation
			continue
		} else if m.provider == "clickhouse" {
			// ClickHouse does not support user-defined enum types at the database level
			continue
		} else if m.provider == "scylla" || m.provider == "scylladb" || m.provider == "cassandra" {
			// ScyllaDB/Cassandra has no user-defined enum types
			continue
		}
	}

	// UP: Create new keyspaces FIRST (ScyllaDB/Cassandra) — UDTs and tables live inside keyspaces
	for _, ks := range diff.NewKeyspaces {
		ksSQL := fmt.Sprintf("CREATE KEYSPACE IF NOT EXISTS \"%s\" WITH REPLICATION = %s", ks.Name, ks.Replication)
		if ks.DurableWrites != nil && !*ks.DurableWrites {
			ksSQL += " AND DURABLE_WRITES = false"
		}
		ksSQL += ";"
		upStatements = append(upStatements, ksSQL)
		hasExecutableSQL = true
		downStatements = append([]string{fmt.Sprintf("DROP KEYSPACE IF EXISTS \"%s\";", ks.Name)}, downStatements...)
	}

	// UP: Create new CQL UDTs (ScyllaDB/Cassandra) — must be before tables that reference them
	for _, udt := range diff.NewUDTs {
		var fields []string
		for _, f := range udt.Fields {
			fields = append(fields, fmt.Sprintf("%s %s", f.Name, f.Type))
		}
		udtSQL := fmt.Sprintf("CREATE TYPE IF NOT EXISTS %s (%s);", udt.Name, strings.Join(fields, ", "))
		upStatements = append(upStatements, udtSQL)
		hasExecutableSQL = true
		downStatements = append([]string{fmt.Sprintf("DROP TYPE IF EXISTS %s;", udt.Name)}, downStatements...)
	}

	// UP: Emit raw passthrough statements (DOMAIN types, composite types, PARTITION OF).
	var rawExtensions []string
	var rawBeforeTables []string
	var rawAfterTables []string
	for _, raw := range diff.NewRawStatements {
		upper := strings.ToUpper(raw)
		if strings.Contains(upper, "CREATE FUNCTION") || strings.Contains(upper, "CREATE OR REPLACE FUNCTION") ||
			strings.Contains(upper, "CREATE TRIGGER") || strings.Contains(upper, "CREATE OR REPLACE TRIGGER") ||
			strings.Contains(upper, "PARTITION OF") {
			rawAfterTables = append(rawAfterTables, raw)
		} else if strings.Contains(upper, "CREATE EXTENSION") {
			rawExtensions = append(rawExtensions, raw)
		} else {
			rawBeforeTables = append(rawBeforeTables, raw)
		}
		hasExecutableSQL = true
	}
	// Extensions must be first (before enums, domains, tables)
	upStatements = append(rawExtensions, upStatements...)
	upStatements = append(upStatements, rawBeforeTables...)

	// UP: Rename tables (detected renames — preserves data, no drop+recreate)
	for _, rename := range diff.RenamedTables {
		var renameSQL, renameDownSQL string
		switch m.provider {
		case "postgresql", "postgres":
			renameSQL = fmt.Sprintf("ALTER TABLE \"%s\" RENAME TO \"%s\";", rename.OldName, rename.NewName)
			renameDownSQL = fmt.Sprintf("ALTER TABLE \"%s\" RENAME TO \"%s\";", rename.NewName, rename.OldName)
		case "mysql":
			renameSQL = fmt.Sprintf("RENAME TABLE `%s` TO `%s`;", rename.OldName, rename.NewName)
			renameDownSQL = fmt.Sprintf("RENAME TABLE `%s` TO `%s`;", rename.NewName, rename.OldName)
		case "sqlite", "sqlite3":
			renameSQL = fmt.Sprintf("ALTER TABLE \"%s\" RENAME TO \"%s\";", rename.OldName, rename.NewName)
			renameDownSQL = fmt.Sprintf("ALTER TABLE \"%s\" RENAME TO \"%s\";", rename.NewName, rename.OldName)
		case "clickhouse":
			renameSQL = fmt.Sprintf("RENAME TABLE `%s` TO `%s`;", rename.OldName, rename.NewName)
			renameDownSQL = fmt.Sprintf("RENAME TABLE `%s` TO `%s`;", rename.NewName, rename.OldName)
		default:
			renameSQL = fmt.Sprintf("ALTER TABLE \"%s\" RENAME TO \"%s\";", rename.OldName, rename.NewName)
			renameDownSQL = fmt.Sprintf("ALTER TABLE \"%s\" RENAME TO \"%s\";", rename.NewName, rename.OldName)
		}
		upStatements = append(upStatements, renameSQL)
		hasExecutableSQL = true
		downStatements = append([]string{renameDownSQL}, downStatements...)
	}

	// UP: Create new tables and their indexes.
	var viewUpStatements []string
	var viewDownStatements []string
	for _, table := range diff.NewTables {
		if len(table.Columns) == 1 && (strings.HasPrefix(table.Columns[0].Name, "/* VIEW */") || strings.HasPrefix(table.Columns[0].Name, "/* MATERIALIZED VIEW */")) {
			viewSQL := table.Columns[0].Type
			isMaterialized := strings.HasPrefix(table.Columns[0].Name, "/* MATERIALIZED VIEW */")
			// Inject IF NOT EXISTS for idempotent apply
			if !strings.Contains(strings.ToUpper(viewSQL), "IF NOT EXISTS") {
				if isMaterialized {
					viewSQL = strings.Replace(viewSQL, "CREATE MATERIALIZED VIEW", "CREATE MATERIALIZED VIEW IF NOT EXISTS", 1)
				} else {
					viewSQL = strings.Replace(viewSQL, "CREATE VIEW", "CREATE VIEW", 1)
				}
			}
			viewUpStatements = append(viewUpStatements, viewSQL)
			if isMaterialized {
				viewDownStatements = append(viewDownStatements, fmt.Sprintf("DROP MATERIALIZED VIEW IF EXISTS \"%s\";", table.Name))
			} else {
				viewDownStatements = append(viewDownStatements, fmt.Sprintf("DROP VIEW IF EXISTS \"%s\";", table.Name))
			}
			// Ensure referenced tables exist: parse the view SQL for table names
			// and emit CREATE TABLE IF NOT EXISTS for any that aren't in newTables.
			if m.provider == "scylla" || m.provider == "scylladb" || m.provider == "cassandra" {
				rawViewSQL := table.Columns[0].Type
				for _, refName := range extractRefTables(rawViewSQL) {
					if !isTableInNewTables(refName, diff.NewTables) {
						refTable := findTableInSchema(refName, m.schemaManager, m.schemaPath)
						if refTable != nil {
							sql := m.adapter.GenerateCreateTableSQL(*refTable)
							if sql != "" {
								upStatements = append(upStatements, sql)
								hasExecutableSQL = true
							}
						}
					}
				}
			}
			continue
		}

		sql := m.adapter.GenerateCreateTableSQL(table)
		if sql != "" {
			upStatements = append(upStatements, sql)
			hasExecutableSQL = true
		}

		for _, index := range table.Indexes {
			if strings.HasPrefix(index.Name, "sqlite_") {
				continue
			}
			indexSQL := m.adapter.GenerateAddIndexSQL(index)
			if indexSQL != "" {
				upStatements = append(upStatements, indexSQL)
				hasExecutableSQL = true
			}
		}

		downStatements = append([]string{dropTableSQL(table.Name)}, downStatements...)
		for _, index := range table.Indexes {
			if strings.HasPrefix(index.Name, "sqlite_") {
				continue
			}
			downStatements = append([]string{dropIndexSQL(index)}, downStatements...)
		}
	}
	// Append views after all tables
	upStatements = append(upStatements, viewUpStatements...)
	hasExecutableSQL = hasExecutableSQL || len(viewUpStatements) > 0
	downStatements = append(viewDownStatements, downStatements...)

	// Append functions/triggers after tables and views
	upStatements = append(upStatements, rawAfterTables...)

	// UP: Modify existing tables
	for _, tableDiff := range diff.ModifiedTables {
		// Handle column renames first (before add/drop to preserve data)
		for _, rename := range tableDiff.RenamedColumns {
			var renameSQL, renameDownSQL string
			switch m.provider {
			case "postgresql", "postgres":
				renameSQL = fmt.Sprintf("ALTER TABLE \"%s\" RENAME COLUMN \"%s\" TO \"%s\";", tableDiff.Name, rename.OldName, rename.NewName)
				renameDownSQL = fmt.Sprintf("ALTER TABLE \"%s\" RENAME COLUMN \"%s\" TO \"%s\";", tableDiff.Name, rename.NewName, rename.OldName)
			case "mysql":
				// MySQL needs the column type in CHANGE COLUMN — find it from the new table
				var colType string
				for _, col := range tableDiff.NewTable.Columns {
					if col.Name == rename.NewName {
						colType = col.Type
						break
					}
				}
				if colType == "" {
					colType = "TEXT"
				}
				renameSQL = fmt.Sprintf("ALTER TABLE `%s` CHANGE COLUMN `%s` `%s` %s;", tableDiff.Name, rename.OldName, rename.NewName, colType)
				renameDownSQL = fmt.Sprintf("ALTER TABLE `%s` CHANGE COLUMN `%s` `%s` %s;", tableDiff.Name, rename.NewName, rename.OldName, colType)
			case "sqlite", "sqlite3":
				renameSQL = fmt.Sprintf("ALTER TABLE \"%s\" RENAME COLUMN \"%s\" TO \"%s\";", tableDiff.Name, rename.OldName, rename.NewName)
				renameDownSQL = fmt.Sprintf("ALTER TABLE \"%s\" RENAME COLUMN \"%s\" TO \"%s\";", tableDiff.Name, rename.NewName, rename.OldName)
			case "clickhouse":
				renameSQL = fmt.Sprintf("ALTER TABLE `%s` RENAME COLUMN `%s` TO `%s`;", tableDiff.Name, rename.OldName, rename.NewName)
				renameDownSQL = fmt.Sprintf("ALTER TABLE `%s` RENAME COLUMN `%s` TO `%s`;", tableDiff.Name, rename.NewName, rename.OldName)
			default:
				renameSQL = fmt.Sprintf("ALTER TABLE \"%s\" RENAME COLUMN \"%s\" TO \"%s\";", tableDiff.Name, rename.OldName, rename.NewName)
				renameDownSQL = fmt.Sprintf("ALTER TABLE \"%s\" RENAME COLUMN \"%s\" TO \"%s\";", tableDiff.Name, rename.NewName, rename.OldName)
			}
			upStatements = append(upStatements, renameSQL)
			hasExecutableSQL = true
			downStatements = append([]string{renameDownSQL}, downStatements...)
		}

		needsSQLiteRecreate := (m.provider == "sqlite" || m.provider == "sqlite3") &&
			len(tableDiff.ModifiedColumns) > 0 &&
			m.hasSignificantSQLiteModifications(tableDiff)

		if !needsSQLiteRecreate {
			// Add new columns
			for _, column := range tableDiff.NewColumns {
				sql := m.adapter.GenerateAddColumnSQL(tableDiff.Name, column)
				if sql != "" {
					upStatements = append(upStatements, sql)
					hasExecutableSQL = true
					// DOWN: Drop the added column
					downStatements = append([]string{m.adapter.GenerateDropColumnSQL(tableDiff.Name, column.Name)}, downStatements...)
				}
			}

			// Drop columns
			for _, column := range tableDiff.DroppedColumns {
				sql := m.adapter.GenerateDropColumnSQL(tableDiff.Name, column.Name)
				if sql != "" {
					upStatements = append(upStatements, sql)
					hasExecutableSQL = true
					// DOWN: Re-add the dropped column with its original definition
					downStatements = append([]string{m.adapter.GenerateAddColumnSQL(tableDiff.Name, column)}, downStatements...)
				}
			}
		}

		// Modified columns
		if len(tableDiff.ModifiedColumns) > 0 {
			if m.provider == "sqlite" || m.provider == "sqlite3" {
				if needsSQLiteRecreate {
					recreateSQL := m.generateSQLiteTableRecreateSQL(tableDiff.OldTable, tableDiff.NewTable)
					if recreateSQL != "" {
						upStatements = append(upStatements, recreateSQL)
						hasExecutableSQL = true
						downRecreate := m.generateSQLiteTableRecreateSQL(tableDiff.NewTable, tableDiff.OldTable)
						downStatements = append([]string{downRecreate}, downStatements...)
					}
				}
			} else {
				for _, colDiff := range tableDiff.ModifiedColumns {
					// 1. Type change
					if colDiff.OldType != colDiff.NewType {
						sql := m.adapter.GenerateAlterColumnSQL(tableDiff.Name, colDiff.NewColumn, colDiff.OldType)
						if sql != "" {
							upStatements = append(upStatements, sql)
							hasExecutableSQL = true
							revertSQL := m.adapter.GenerateAlterColumnSQL(tableDiff.Name, colDiff.OldColumn, colDiff.NewType)
							if revertSQL != "" {
								downStatements = append([]string{revertSQL}, downStatements...)
							}
						}
					}

					// 2. Nullable change (PostgreSQL / MySQL)
					if colDiff.NullableChanged {
						var nullSQL, nullDownSQL string
						switch m.provider {
						case "postgresql", "postgres":
							if colDiff.NewColumn.Nullable {
								nullSQL = fmt.Sprintf("ALTER TABLE \"%s\" ALTER COLUMN \"%s\" DROP NOT NULL;", tableDiff.Name, colDiff.Name)
								nullDownSQL = fmt.Sprintf("ALTER TABLE \"%s\" ALTER COLUMN \"%s\" SET NOT NULL;", tableDiff.Name, colDiff.Name)
							} else {
								nullSQL = fmt.Sprintf("ALTER TABLE \"%s\" ALTER COLUMN \"%s\" SET NOT NULL;", tableDiff.Name, colDiff.Name)
								nullDownSQL = fmt.Sprintf("ALTER TABLE \"%s\" ALTER COLUMN \"%s\" DROP NOT NULL;", tableDiff.Name, colDiff.Name)
							}
						case "mysql":
							// MySQL MODIFY COLUMN already handles nullable in GenerateAlterColumnSQL
						}
						if nullSQL != "" {
							upStatements = append(upStatements, nullSQL)
							hasExecutableSQL = true
							downStatements = append([]string{nullDownSQL}, downStatements...)
						}
					}

					// 3. Default change (PostgreSQL / MySQL)
					if colDiff.DefaultChanged {
						var defSQL, defDownSQL string
						switch m.provider {
						case "postgresql", "postgres":
							if colDiff.NewColumn.Default != "" {
								defSQL = fmt.Sprintf("ALTER TABLE \"%s\" ALTER COLUMN \"%s\" SET DEFAULT %s;", tableDiff.Name, colDiff.Name, colDiff.NewColumn.Default)
								if colDiff.OldColumn.Default != "" {
									defDownSQL = fmt.Sprintf("ALTER TABLE \"%s\" ALTER COLUMN \"%s\" SET DEFAULT %s;", tableDiff.Name, colDiff.Name, colDiff.OldColumn.Default)
								} else {
									defDownSQL = fmt.Sprintf("ALTER TABLE \"%s\" ALTER COLUMN \"%s\" DROP DEFAULT;", tableDiff.Name, colDiff.Name)
								}
							} else {
								defSQL = fmt.Sprintf("ALTER TABLE \"%s\" ALTER COLUMN \"%s\" DROP DEFAULT;", tableDiff.Name, colDiff.Name)
								if colDiff.OldColumn.Default != "" {
									defDownSQL = fmt.Sprintf("ALTER TABLE \"%s\" ALTER COLUMN \"%s\" SET DEFAULT %s;", tableDiff.Name, colDiff.Name, colDiff.OldColumn.Default)
								}
							}
						case "mysql":
							// MySQL MODIFY COLUMN already handles default in GenerateAlterColumnSQL
						}
						if defSQL != "" {
							upStatements = append(upStatements, defSQL)
							hasExecutableSQL = true
							if defDownSQL != "" {
								downStatements = append([]string{defDownSQL}, downStatements...)
							}
						}
					}

					// 4. CHECK constraint change
					if colDiff.OldColumn.Check != colDiff.NewColumn.Check {
						constraintName := fmt.Sprintf("%s_%s_check", tableDiff.Name, colDiff.Name)
						switch m.provider {
						case "postgresql", "postgres":
							if colDiff.OldColumn.Check != "" {
								// Drop old CHECK
								dropCheck := fmt.Sprintf("ALTER TABLE \"%s\" DROP CONSTRAINT IF EXISTS \"%s\";", tableDiff.Name, constraintName)
								upStatements = append(upStatements, dropCheck)
								hasExecutableSQL = true
								if colDiff.NewColumn.Check == "" {
									downStatements = append([]string{fmt.Sprintf("ALTER TABLE \"%s\" ADD CONSTRAINT \"%s\" CHECK (%s);", tableDiff.Name, constraintName, colDiff.OldColumn.Check)}, downStatements...)
								}
							}
							if colDiff.NewColumn.Check != "" {
								// Add new CHECK
								addCheck := fmt.Sprintf("ALTER TABLE \"%s\" ADD CONSTRAINT \"%s\" CHECK (%s);", tableDiff.Name, constraintName, colDiff.NewColumn.Check)
								upStatements = append(upStatements, addCheck)
								hasExecutableSQL = true
								downStatements = append([]string{fmt.Sprintf("ALTER TABLE \"%s\" DROP CONSTRAINT IF EXISTS \"%s\";", tableDiff.Name, constraintName)}, downStatements...)
							}
						case "mysql":
							if colDiff.OldColumn.Check != "" {
								upStatements = append(upStatements, fmt.Sprintf("ALTER TABLE `%s` DROP CHECK `%s`;", tableDiff.Name, constraintName))
								hasExecutableSQL = true
							}
							if colDiff.NewColumn.Check != "" {
								upStatements = append(upStatements, fmt.Sprintf("ALTER TABLE `%s` ADD CONSTRAINT `%s` CHECK (%s);", tableDiff.Name, constraintName, colDiff.NewColumn.Check))
								hasExecutableSQL = true
							}
						}
					}

					// 5. UNIQUE change (PostgreSQL / MySQL — SQLite handled by table recreate)
					if !colDiff.OldColumn.IsUnique && colDiff.NewColumn.IsUnique {
						indexName := fmt.Sprintf("%s_%s_key", tableDiff.Name, colDiff.Name)
						switch m.provider {
						case "postgresql", "postgres":
							upStatements = append(upStatements, fmt.Sprintf("ALTER TABLE \"%s\" ADD CONSTRAINT \"%s\" UNIQUE (\"%s\");", tableDiff.Name, indexName, colDiff.Name))
							downStatements = append([]string{fmt.Sprintf("ALTER TABLE \"%s\" DROP CONSTRAINT IF EXISTS \"%s\";", tableDiff.Name, indexName)}, downStatements...)
						case "mysql":
							upStatements = append(upStatements, fmt.Sprintf("ALTER TABLE `%s` ADD UNIQUE INDEX `%s` (`%s`);", tableDiff.Name, indexName, colDiff.Name))
							downStatements = append([]string{fmt.Sprintf("ALTER TABLE `%s` DROP INDEX `%s`;", tableDiff.Name, indexName)}, downStatements...)
						}
						hasExecutableSQL = true
					} else if colDiff.OldColumn.IsUnique && !colDiff.NewColumn.IsUnique {
						indexName := fmt.Sprintf("%s_%s_key", tableDiff.Name, colDiff.Name)
						switch m.provider {
						case "postgresql", "postgres":
							upStatements = append(upStatements, fmt.Sprintf("ALTER TABLE \"%s\" DROP CONSTRAINT IF EXISTS \"%s\";", tableDiff.Name, indexName))
							downStatements = append([]string{fmt.Sprintf("ALTER TABLE \"%s\" ADD CONSTRAINT \"%s\" UNIQUE (\"%s\");", tableDiff.Name, indexName, colDiff.Name)}, downStatements...)
						case "mysql":
							upStatements = append(upStatements, fmt.Sprintf("ALTER TABLE `%s` DROP INDEX `%s`;", tableDiff.Name, indexName))
							downStatements = append([]string{fmt.Sprintf("ALTER TABLE `%s` ADD UNIQUE INDEX `%s` (`%s`);", tableDiff.Name, indexName, colDiff.Name)}, downStatements...)
						}
						hasExecutableSQL = true
					}

					// 6. GENERATED expression change (PostgreSQL only)
					if colDiff.GeneratedChanged {
						switch m.provider {
						case "postgresql", "postgres":
							// Drop the old generated column and re-add with new expression
							dropSQL := m.adapter.GenerateDropColumnSQL(tableDiff.Name, colDiff.Name)
							upStatements = append(upStatements, dropSQL)
							hasExecutableSQL = true
							addSQL := m.adapter.GenerateAddColumnSQL(tableDiff.Name, colDiff.NewColumn)
							if addSQL != "" {
								upStatements = append(upStatements, addSQL)
							}
							// DOWN: drop new and re-add old
							downStatements = append([]string{
								m.adapter.GenerateDropColumnSQL(tableDiff.Name, colDiff.Name),
								m.adapter.GenerateAddColumnSQL(tableDiff.Name, colDiff.OldColumn),
							}, downStatements...)
						case "mysql":
							// MySQL: MODIFY COLUMN with GENERATED ALWAYS AS
							genSQL := fmt.Sprintf("ALTER TABLE `%s` MODIFY COLUMN `%s` %s GENERATED ALWAYS AS (%s) STORED;",
								tableDiff.Name, colDiff.Name, colDiff.NewColumn.Type, colDiff.NewColumn.Generated)
							upStatements = append(upStatements, genSQL)
							hasExecutableSQL = true
							genDown := fmt.Sprintf("ALTER TABLE `%s` MODIFY COLUMN `%s` %s GENERATED ALWAYS AS (%s) STORED;",
								tableDiff.Name, colDiff.Name, colDiff.OldColumn.Type, colDiff.OldColumn.Generated)
							downStatements = append([]string{genDown}, downStatements...)
						}
					}
				}
			}
		}
	}

	// UP: Drop tables
	for _, tableName := range diff.DroppedTables {
		upStatements = append(upStatements, dropTableSQL(tableName))
		hasExecutableSQL = true
		// DOWN: Restore the dropped table using schema snapshot info
		if m.schemaManager != nil {
			oldTable := findTableInSchema(tableName, m.schemaManager, m.schemaPath)
			if oldTable != nil {
				restoreSQL := m.adapter.GenerateCreateTableSQL(*oldTable)
				if restoreSQL != "" {
					downStatements = append([]string{restoreSQL}, downStatements...)
				}
			}
		}
	}

	// UP: Drop enums
	for _, enumName := range diff.DroppedEnums {
		if m.provider == "clickhouse" || m.provider == "sqlite" || m.provider == "sqlite3" || m.provider == "mysql" || m.provider == "scylla" || m.provider == "scylladb" || m.provider == "cassandra" {
			continue
		}
		upStatements = append(upStatements, fmt.Sprintf("DROP TYPE IF EXISTS \"%s\";", enumName))
		hasExecutableSQL = true
		// DOWN: Recreate the dropped enum if we have its definition from the schema snapshot
		if m.schemaManager != nil {
			snapshot, err := schema.LoadSchemaSnapshot(m.migrationsDir)
			if err == nil {
				for _, e := range snapshot.Enums {
					if strings.EqualFold(e.Name, enumName) && len(e.Values) > 0 {
						values := make([]string, len(e.Values))
						for i, v := range e.Values {
							values[i] = fmt.Sprintf("'%s'", strings.ReplaceAll(v, "'", "''"))
						}
						downStatements = append([]string{fmt.Sprintf("CREATE TYPE \"%s\" AS ENUM (%s);", enumName, strings.Join(values, ", "))}, downStatements...)
						break
					}
				}
			}
		}
	}

	// UP: Add values to existing enums (PostgreSQL only — ALTER TYPE ... ADD VALUE)
	for _, enumDiff := range diff.ModifiedEnums {
		for _, val := range enumDiff.AddValues {
			escaped := strings.ReplaceAll(val, "'", "''")
			if m.provider == "postgresql" || m.provider == "postgres" {
				sql := fmt.Sprintf("ALTER TYPE \"%s\" ADD VALUE IF NOT EXISTS '%s';", enumDiff.Name, escaped)
				upStatements = append(upStatements, sql)
				hasExecutableSQL = true
				downStatements = append([]string{fmt.Sprintf("-- Cannot remove enum value '%s' from \"%s\" (PostgreSQL limitation)", val, enumDiff.Name)}, downStatements...)
			}
		}
	}

	// Handle standalone index changes (drop first to avoid conflicts).
	for _, index := range diff.DroppedIndexes {
		upStatements = append(upStatements, m.adapter.GenerateDropIndexSQL(index))
		hasExecutableSQL = true
	}

	// UP: Add new indexes
	for _, index := range diff.NewIndexes {
		if strings.HasPrefix(index.Name, "sqlite_") {
			continue
		}
		indexSQL := m.adapter.GenerateAddIndexSQL(index)
		if indexSQL != "" {
			upStatements = append(upStatements, indexSQL)
			hasExecutableSQL = true
			downStatements = append([]string{fmt.Sprintf("DROP INDEX IF EXISTS \"%s\";", index.Name)}, downStatements...)
		}
	}

	return m.formatMigrationFileWithDown(name, upStatements, downStatements), hasExecutableSQL
}

func (m *Migrator) generateEmptyMigrationTemplate(name string) string {
	upStatements := []string{
		"-- Add your SQL statements here",
		"-- Example: CREATE TABLE users (id SERIAL PRIMARY KEY, name VARCHAR(255) NOT NULL);",
	}

	return m.formatMigrationFile(name, upStatements)
}

func (m *Migrator) formatMigrationFile(name string, upStatements []string) string {
	return m.formatMigrationFileWithDown(name, upStatements, nil)
}

func (m *Migrator) formatMigrationFileWithDown(name string, upStatements []string, downStatements []string) string {
	timestamp := time.Now().Format("2006-01-02T15:04:05Z")

	var builder strings.Builder

	builder.WriteString(fmt.Sprintf("-- Migration: %s\n", name))
	builder.WriteString(fmt.Sprintf("-- Created: %s\n\n", timestamp))

	// UP section
	builder.WriteString("-- +migrate Up\n")
	if len(upStatements) > 0 {
		for i, stmt := range upStatements {
			trimmed := strings.TrimSpace(stmt)
			builder.WriteString(trimmed)
			if !strings.HasSuffix(trimmed, ";") {
				builder.WriteString(";")
			}
			builder.WriteString("\n")
			if i < len(upStatements)-1 {
				builder.WriteString("\n")
			}
		}
	} else {
		builder.WriteString("-- No migration statements\n")
	}

	// DOWN section
	builder.WriteString("\n-- +migrate Down\n")
	if len(downStatements) > 0 {
		for i, stmt := range downStatements {
			trimmed := strings.TrimSpace(stmt)
			builder.WriteString(trimmed)
			if !strings.HasSuffix(strings.TrimSpace(trimmed), ";") && !strings.HasPrefix(trimmed, "--") {
				builder.WriteString(";")
			}
			// builder.WriteString("\n")
			if i < len(downStatements)-1 {
				builder.WriteString("\n")
			}
		}
	} else {
		builder.WriteString("-- Add rollback statements here\n")
	}

	return builder.String()
}

func (m *Migrator) PullSchema(ctx context.Context) ([]types.SchemaTable, error) {
	return m.adapter.GetCurrentSchema(ctx)
}

// generateSQLiteTableRecreateSQL generates the multi-statement SQL required to
// recreate a SQLite table when columns are modified (since SQLite does not
// support ALTER COLUMN). The pattern is:
//  1. Create a temporary table with the new schema
//  2. Copy data from old to new (matching columns only)
//  3. Drop the old table
//  4. Rename the temporary table
//  5. Recreate indexes
func (m *Migrator) generateSQLiteTableRecreateSQL(oldTable, newTable types.SchemaTable) string {
	var parts []string

	parts = append(parts, "PRAGMA foreign_keys=OFF;")

	// Create temporary table with the desired schema
	tempTable := newTable
	tempTable.Name = newTable.Name + "_new"
	tempTable.Indexes = nil // Indexes added after rename
	createSQL := m.adapter.GenerateCreateTableSQL(tempTable)
	// Replace "IF NOT EXISTS" with plain CREATE for clarity
	createSQL = strings.Replace(createSQL, "CREATE TABLE IF NOT EXISTS", "CREATE TABLE", 1)
	parts = append(parts, createSQL)

	// Build list of columns common to both tables for the INSERT
	oldColMap := make(map[string]bool, len(oldTable.Columns))
	for _, col := range oldTable.Columns {
		oldColMap[col.Name] = true
	}
	var commonCols []string
	for _, col := range newTable.Columns {
		if oldColMap[col.Name] {
			commonCols = append(commonCols, fmt.Sprintf(`"%s"`, col.Name))
		}
	}

	if len(commonCols) > 0 {
		cols := strings.Join(commonCols, ", ")
		parts = append(parts, fmt.Sprintf(
			`INSERT INTO "%s" (%s) SELECT %s FROM "%s";`,
			tempTable.Name, cols, cols, oldTable.Name,
		))
	}

	parts = append(parts, fmt.Sprintf(`DROP TABLE "%s";`, oldTable.Name))
	parts = append(parts, fmt.Sprintf(`ALTER TABLE "%s" RENAME TO "%s";`, tempTable.Name, newTable.Name))

	// Recreate standalone indexes
	for _, index := range newTable.Indexes {
		if strings.HasPrefix(index.Name, "sqlite_") {
			continue
		}
		idxSQL := m.adapter.GenerateAddIndexSQL(index)
		if idxSQL != "" {
			parts = append(parts, idxSQL)
		}
	}

	parts = append(parts, "PRAGMA foreign_keys=ON;")

	return strings.Join(parts, "\n")
}

// hasSignificantSQLiteModifications checks if any ModifiedColumn in the table diff
// represents a real semantic type change (e.g., TEXT → INTEGER) rather than a
// cosmetic one (e.g., TEXT → VARCHAR(255)) for SQLite.
func (m *Migrator) hasSignificantSQLiteModifications(tableDiff types.TableDiff) bool {
	for _, col := range tableDiff.ModifiedColumns {
		oldNorm := m.adapter.MapColumnType(col.OldType)
		newNorm := m.adapter.MapColumnType(col.NewType)
		if oldNorm != newNorm {
			return true
		}
		if col.OldColumn.Nullable != col.NewColumn.Nullable {
			return true
		}
		if col.OldColumn.Default != col.NewColumn.Default {
			return true
		}
		if col.OldColumn.IsPrimary != col.NewColumn.IsPrimary {
			return true
		}
		if col.OldColumn.IsUnique != col.NewColumn.IsUnique {
			return true
		}
		if col.OldColumn.Check != col.NewColumn.Check {
			return true
		}
		if col.OldColumn.ForeignKeyTable != col.NewColumn.ForeignKeyTable {
			return true
		}
		if col.OldColumn.ForeignKeyColumn != col.NewColumn.ForeignKeyColumn {
			return true
		}
	}
	return false
}

func (m *Migrator) GenerateEmptyMigration(ctx context.Context, name string) error {
	filename := m.fileUtils.GenerateMigrationFilename(name)
	filepath := filepath.Join(m.migrationsDir, filename)

	sqlContent := m.generateEmptyMigrationTemplate(name)

	if err := os.WriteFile(filepath, []byte(sqlContent), 0644); err != nil {
		return fmt.Errorf("failed to write migration file: %w", err)
	}

	fmt.Printf("Generated empty migration: %s\n", filename)
	return nil
}
