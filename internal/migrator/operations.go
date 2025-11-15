package migrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Lumos-Labs-HQ/flash/internal/types"
	"github.com/Lumos-Labs-HQ/flash/internal/utils"
)

// Apply runs migrations with optional generation
func (m *Migrator) Apply(ctx context.Context, name, schemaPath string) error {
	if err := m.createMigrationsTable(ctx); err != nil {
		return fmt.Errorf("failed to create migrations table: %w", err)
	}

	if name != "" {
		if err := m.GenerateMigration(ctx, name, schemaPath); err != nil {
			return fmt.Errorf("failed to generate migration: %w", err)
		}
	}

	return m.ApplyWithConflictDetection(ctx)
}

// ApplyWithConflictDetection applies pending migrations with conflict detection
func (m *Migrator) ApplyWithConflictDetection(ctx context.Context) error {
	_ = m.cleanupBrokenMigrationRecords(ctx) 

	migrations, err := m.loadMigrationsFromDir()
	if err != nil {
		return fmt.Errorf("failed to load migrations: %w", err)
	}

	applied, err := m.getAppliedMigrations(ctx)
	if err != nil {
		return fmt.Errorf("failed to get applied migrations: %w", err)
	}

	pending := utils.FilterPendingMigrations(migrations, applied)
	if len(pending) == 0 {
		fmt.Println("No pending migrations")
		return nil
	}

	fmt.Printf("Found %d pending migrations\n", len(pending))

	if hasConflicts, conflicts, err := m.hasConflicts(ctx, pending); err != nil {
		return fmt.Errorf("failed to check for conflicts: %w", err)
	} else if hasConflicts {
		return m.handleConflictsInteractively(ctx, conflicts, pending)
	}

	return m.applyMigrations(ctx, pending)
}

// handleConflictsInteractively handles migration conflicts interactively
func (m *Migrator) handleConflictsInteractively(ctx context.Context, conflicts []types.MigrationConflict, pending []types.Migration) error {
	fmt.Println("⚠️  Migration conflicts detected:")
	for _, c := range conflicts {
		fmt.Printf("  - %s\n", c.Description)
	}
	fmt.Println()

	if m.force {
		fmt.Println("🚀 Force flag detected - resetting database and applying migrations...")
		return m.handleResetAndApply(ctx)
	}

	input := &utils.InputUtils{}
	choice := input.GetUserChoice([]string{"y", "n"}, "Reset database to resolve conflicts? This will drop all tables and data", false)

	if strings.ToLower(choice) != "y" {
		fmt.Println("Migration aborted due to conflicts")
		return fmt.Errorf("migration aborted due to conflicts")
	}

	if input.GetUserChoice([]string{"y", "n"}, "Create export before applying?", false) == "y" {
		fmt.Println("📦 Creating export...")
		if err := m.createExport(); err != nil {
			fmt.Printf("⚠️  Export failed: %v\n   Continuing without export...\n", err)
		} else {
			fmt.Println("✅ Export created successfully")
		}
	}

	return m.handleResetAndApply(ctx)
}

// handleResetAndApply resets DB and applies all migrations
func (m *Migrator) handleResetAndApply(ctx context.Context) error {
	fmt.Println("🔄 Resetting database and applying all migrations...")
	tables, err := m.adapter.GetAllTableNames(ctx)
	if err != nil {
		return fmt.Errorf("failed to get table names: %w", err)
	}

	for _, table := range tables {
		if err := m.adapter.DropTable(ctx, table); err != nil {
			fmt.Printf("Warning: Failed to drop table %s: %v\n", table, err)
		}
	}

	if err := m.createMigrationsTable(ctx); err != nil {
		return fmt.Errorf("failed to recreate migrations table: %w", err)
	}

	allMigrations, err := m.loadMigrationsFromDir()
	if err != nil {
		return fmt.Errorf("failed to load migrations: %w", err)
	}

	return m.applyMigrations(ctx, allMigrations)
}

// applyMigrations applies migrations safely - each in its own transaction
func (m *Migrator) applyMigrations(ctx context.Context, migrations []types.Migration) error {
	if len(migrations) == 0 {
		return nil
	}

	fmt.Printf("📦 Applying %d migration(s)...\n", len(migrations))

	for i, migration := range migrations {
		fmt.Printf("  [%d/%d] %s\n", i+1, len(migrations), migration.ID)

		if err := m.applySingleMigrationSafely(ctx, migration); err != nil {
			fmt.Printf("❌ Failed at migration: %s\n", migration.ID)
			fmt.Printf("   Error: %v\n", err)
			fmt.Println("   Transaction rolled back. Fix the error and run 'flash apply' again.")
			return fmt.Errorf("migration %s failed: %w", migration.ID, err)
		}

		fmt.Printf("      ✅ Applied\n")
	}

	fmt.Println("✅ All migrations applied successfully")
	return nil
}

// applySingleMigrationSafely applies migration and records it in a single transaction
func (m *Migrator) applySingleMigrationSafely(ctx context.Context, migration types.Migration) error {
	content, err := os.ReadFile(migration.FilePath)
	if err != nil {
		return fmt.Errorf("failed to read migration file: %w", err)
	}

	checksum := fmt.Sprintf("%x", len(content))
	
	// Use the combined method that does both operations in a single transaction
	if err := m.adapter.ExecuteAndRecordMigration(ctx, migration.ID, migration.Name, checksum, string(content)); err != nil {
		return err
	}

	return nil
}

// createExport creates a database export using the adapter
func (m *Migrator) createExport() error {
	ctx := context.Background()

	tables, err := m.adapter.GetAllTableNames(ctx)
	if err != nil {
		return fmt.Errorf("failed to get table names: %w", err)
	}

	var dataTables []string
	for _, table := range tables {
		if table != "_flash_migrations" {
			dataTables = append(dataTables, table)
		}
	}

	if len(dataTables) == 0 {
		return nil
	}

	exportData := types.BackupData{
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
		Version:   "1.0",
		Tables:    make(map[string]interface{}),
		Comment:   "Pre-conflict export",
	}

	for _, table := range dataTables {
		data, err := m.adapter.GetTableData(ctx, table)
		if err != nil {
			fmt.Printf("Warning: Failed to export table %s: %v\n", table, err)
			continue
		}
		if len(data) > 0 {
			exportData.Tables[table] = data
		}
	}

	exportDir := "db_export"
	if err := os.MkdirAll(exportDir, 0755); err != nil {
		return fmt.Errorf("failed to create export directory: %w", err)
	}

	filename := fmt.Sprintf("export_%s.json",
		time.Now().Format("2006-01-02_15-04-05"))
	exportPath := filepath.Join(exportDir, filename)

	data, err := json.MarshalIndent(exportData, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal export data: %w", err)
	}

	if err := os.WriteFile(exportPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write export file: %w", err)
	}

	fmt.Printf("✅ Export saved to: %s\n", exportPath)
	return nil
}

// Reset drops all tables and optionally exports data
func (m *Migrator) Reset(ctx context.Context, force bool) error {
	fmt.Println("🗑️  This will drop all tables and data!")

	// Skip confirmation if force flag is set
	if !force {
		if !m.askUserConfirmation("Are you sure you want to reset the database?") {
			fmt.Println("Database reset cancelled")
			return nil
		}

		if m.askUserConfirmation("Create export before reset?") {
			fmt.Println("📦 Creating export...")
			if err := m.createExport(); err != nil {
				fmt.Printf("⚠️  Export failed: %v\n", err)
			}
		}
	} else {
		fmt.Println("⚡ Force mode: Skipping confirmations and backup")
	}

	// Drop all tables first
	tables, err := m.adapter.GetAllTableNames(ctx)
	if err != nil {
		return fmt.Errorf("failed to get table names: %w", err)
	}

	m.adapter.ExecuteMigration(ctx, "SET FOREIGN_KEY_CHECKS = 0")

	for _, table := range tables {
		if err := m.adapter.DropTable(ctx, table); err != nil {
			fmt.Printf("Warning: Failed to drop table %s: %v\n", table, err)
		}
	}

	m.adapter.ExecuteMigration(ctx, "SET FOREIGN_KEY_CHECKS = 1")

	// Drop all enums
	enums, err := m.adapter.GetCurrentEnums(ctx)
	if err == nil {
		for _, enum := range enums {
			if err := m.adapter.DropEnum(ctx, enum.Name); err != nil {
				fmt.Printf("Warning: Failed to drop enum %s: %v\n", enum.Name, err)
			}
		}
	}

	fmt.Println("✅ Database reset completed")
	return nil
}

// Status prints migration status
func (m *Migrator) Status(ctx context.Context) error {
	if err := m.createMigrationsTable(ctx); err != nil {
		return fmt.Errorf("failed to create migrations table: %w", err)
	}

	migrations, err := m.loadMigrationsFromDir()
	if err != nil {
		return fmt.Errorf("failed to load migrations: %w", err)
	}

	applied, err := m.getAppliedMigrations(ctx)
	if err != nil {
		return fmt.Errorf("failed to get applied migrations: %w", err)
	}

	pendingCount := 0
	for _, migration := range migrations {
		if _, exists := applied[migration.ID]; !exists {
			pendingCount++
		}
	}

	fmt.Println("🗂️  Migration Status")
	fmt.Println("==================")
	fmt.Printf("Total: %d | Applied: %d | Pending: %d\n\n", len(migrations), len(applied), pendingCount)

	if len(migrations) == 0 && len(applied) == 0 {
		fmt.Println("No migrations found")
		return nil
	}

	if len(migrations) == 0 && len(applied) > 0 {
		fmt.Println("⚠️  Warning: No migration files found, but database has applied migrations.")
		fmt.Println("   This usually means migration files were deleted.")
		fmt.Println("\nApplied migrations in database:")
		for id, t := range applied {
			timestamp := ""
			if t != nil {
				timestamp = fmt.Sprintf(" (applied: %s)", t.Format("2006-01-02 15:04:05"))
			}
			fmt.Printf("%-50s Applied%s\n", id, timestamp)
		}
		return nil
	}

	fmt.Println("Migration Details:")
	for _, migration := range migrations {
		status := "Pending"
		timestamp := ""
		if t, exists := applied[migration.ID]; exists && t != nil {
			status = "Applied"
			timestamp = fmt.Sprintf(" (applied: %s)", t.Format("2006-01-02 15:04:05"))
		}
		fmt.Printf("%-50s %s%s\n", migration.ID, status, timestamp)
	}

	// Check for orphaned migrations in database
	orphanedCount := 0
	for id := range applied {
		found := false
		for _, migration := range migrations {
			if migration.ID == id {
				found = true
				break
			}
		}
		if !found {
			orphanedCount++
		}
	}

	if orphanedCount > 0 {
		fmt.Printf("\n⚠️  Warning: %d migration(s) in database have no corresponding file\n", orphanedCount)
	}

	return nil
}
