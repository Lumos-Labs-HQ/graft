package migrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Lumos-Labs-HQ/flash/internal/config"
	"github.com/Lumos-Labs-HQ/flash/internal/database"
	"github.com/Lumos-Labs-HQ/flash/internal/schema"
	"github.com/Lumos-Labs-HQ/flash/internal/types"
	"github.com/Lumos-Labs-HQ/flash/internal/utils"
)

type Migrator struct {
	adapter       database.DatabaseAdapter
	schemaManager *schema.SchemaManager
	migrationsDir string
	schemaPath    string
	force         bool
	fileUtils     *utils.FileUtils
	inputUtils    *utils.InputUtils
	conflictUtils *utils.ConflictUtils
}

func NewMigrator(cfg *config.Config) (*Migrator, error) {
	adapter := database.NewAdapter(cfg.Database.Provider)

	dbURL, err := cfg.GetDatabaseURL()
	if err != nil {
		return nil, fmt.Errorf("failed to get database URL: %w", err)
	}

	if err := adapter.Connect(context.Background(), dbURL); err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return &Migrator{
		adapter:       adapter,
		schemaManager: schema.NewSchemaManager(adapter),
		migrationsDir: cfg.MigrationsPath,
		schemaPath:    cfg.SchemaPath,
		force:         false,
		fileUtils:     &utils.FileUtils{},
		inputUtils:    &utils.InputUtils{},
		conflictUtils: &utils.ConflictUtils{},
	}, nil
}

func (m *Migrator) Close() error {
	return m.adapter.Close()
}

func (m *Migrator) SetForce(force bool) {
	m.force = force
}

// Core migration operations - simplified using utils
func (m *Migrator) createMigrationsTable(ctx context.Context) error {
	return m.adapter.CreateMigrationsTable(ctx)
}

func (m *Migrator) getAppliedMigrations(ctx context.Context) (map[string]*time.Time, error) {
	return m.adapter.GetAppliedMigrations(ctx)
}

func (m *Migrator) loadMigrationsFromDir() ([]types.Migration, error) {
	return m.fileUtils.LoadMigrationsFromDir(m.migrationsDir)
}

func (m *Migrator) hasConflicts(ctx context.Context, pendingMigrations []types.Migration) (bool, []types.MigrationConflict, error) {
	var allConflicts []types.MigrationConflict

	for _, migration := range pendingMigrations {
		conflicts, err := m.conflictUtils.DetectMigrationConflicts(ctx, migration, m.adapter)
		if err != nil {
			return false, nil, fmt.Errorf("failed to detect conflicts for migration %s: %w", migration.ID, err)
		}
		allConflicts = append(allConflicts, conflicts...)
	}

	return len(allConflicts) > 0, allConflicts, nil
}

func (m *Migrator) cleanupBrokenMigrationRecords(ctx context.Context) error {
	return m.adapter.CleanupBrokenMigrationRecords(ctx)
}

// GenerateMigration creates a new migration file - simplified
func (m *Migrator) GenerateMigration(ctx context.Context, name string, schemaPath string) error {
	if schemaPath == "" {
		schemaPath = m.schemaPath
	}

	diff, err := m.schemaManager.GenerateSchemaDiff(ctx, schemaPath)
	if err != nil {
		return fmt.Errorf("failed to generate schema diff: %w", err)
	}

	filename := m.fileUtils.GenerateMigrationFilename(name)
	filepath := filepath.Join(m.migrationsDir, filename)

	var sqlContent string
	if len(diff.NewTables) == 0 && len(diff.DroppedTables) == 0 && len(diff.ModifiedTables) == 0 && len(diff.NewEnums) == 0 && len(diff.DroppedEnums) == 0 {
		fmt.Println("No changes detected in schema, creating empty migration template")
		sqlContent = m.generateEmptyMigrationTemplate(name)
	} else {
		sqlContent = m.generateSQLFromDiff(diff, name)
	}

	if err := os.WriteFile(filepath, []byte(sqlContent), 0644); err != nil {
		return fmt.Errorf("failed to write migration file: %w", err)
	}

	fmt.Printf("Generated migration: %s\n", filename)
	return nil
}

// generateSQLFromDiff creates SQL from schema differences - simplified
func (m *Migrator) generateSQLFromDiff(diff *types.SchemaDiff, name string) string {
	var upStatements []string

	for _, enum := range diff.NewEnums {
		values := make([]string, len(enum.Values))
		for i, v := range enum.Values {
			values[i] = fmt.Sprintf("'%s'", v)
		}
		// Use DO block to check if ENUM exists before creating
		enumSQL := fmt.Sprintf(`DO $$ 
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = '%s') THEN
        CREATE TYPE "%s" AS ENUM (%s);
    END IF;
END $$;`, enum.Name, enum.Name, strings.Join(values, ", "))
		upStatements = append(upStatements, enumSQL)
	}

	// Generate UP migration only
	for _, table := range diff.NewTables {
		sql := m.adapter.GenerateCreateTableSQL(table)
		if sql != "" {
			upStatements = append(upStatements, sql)
		}
	}

	for _, tableDiff := range diff.ModifiedTables {
		// Add new columns
		for _, column := range tableDiff.NewColumns {
			sql := m.adapter.GenerateAddColumnSQL(tableDiff.Name, column)
			if sql != "" {
				upStatements = append(upStatements, sql)
			}
		}

		for _, columnName := range tableDiff.DroppedColumns {
			sql := m.adapter.GenerateDropColumnSQL(tableDiff.Name, columnName)
			if sql != "" {
				upStatements = append(upStatements, sql)
			}
		}
	}

	for _, tableName := range diff.DroppedTables {
		upStatements = append(upStatements, fmt.Sprintf("DROP TABLE IF EXISTS \"%s\";", tableName))
	}

	for _, enumName := range diff.DroppedEnums {
		upStatements = append(upStatements, fmt.Sprintf("DROP TYPE IF EXISTS \"%s\";", enumName))
	}

	return m.formatMigrationFile(name, upStatements)
}

func (m *Migrator) generateEmptyMigrationTemplate(name string) string {
	upStatements := []string{
		"-- Add your SQL statements here",
		"-- Example: CREATE TABLE users (id SERIAL PRIMARY KEY, name VARCHAR(255) NOT NULL);",
	}

	return m.formatMigrationFile(name, upStatements)
}

func (m *Migrator) formatMigrationFile(name string, upStatements []string) string {
	timestamp := time.Now().Format("2006-01-02T15:04:05Z")

	var builder strings.Builder

	builder.WriteString(fmt.Sprintf("-- Migration: %s\n", name))
	builder.WriteString(fmt.Sprintf("-- Created: %s\n\n", timestamp))

	if len(upStatements) > 0 {
		for _, stmt := range upStatements {
			builder.WriteString(stmt)
			if !strings.HasSuffix(stmt, ";") {
				builder.WriteString(";")
			}
			builder.WriteString("\n")
		}
	} else {
		builder.WriteString("-- No migration statements\n")
	}

	return builder.String()
}

func (m *Migrator) PullSchema(ctx context.Context) ([]types.SchemaTable, error) {
	return m.adapter.GetCurrentSchema(ctx)
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

func (m *Migrator) askUserConfirmation(message string) bool {
	return m.inputUtils.AskConfirmation(message, m.force)
}
