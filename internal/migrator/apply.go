package migrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

	// Ghost-state detection: if all migrations appear applied but the database
	// has no user tables, the tracking table is stale (e.g. DB was wiped and
	// recreated, or the URL was changed to a db that previously ran migrations
	// but was then reset). Re-apply everything from scratch.
	if len(pending) == 0 && len(applied) > 0 && len(migrations) > 0 {
		if empty, checkErr := m.isDatabaseEmpty(ctx); checkErr == nil && empty {
			if err := m.clearMigrationRecords(ctx); err != nil {
				return fmt.Errorf("failed to clear stale migration records: %w", err)
			}
			pending = migrations
		}
	}

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

	// Parallel drop for independent tables (FK checks disabled for MySQL, CASCADE for PostgreSQL)
	var dropWg sync.WaitGroup
	var dropMu sync.Mutex
	for _, table := range tables {
		dropWg.Add(1)
		go func(t string) {
			defer dropWg.Done()
			if err := m.adapter.DropTable(ctx, t); err != nil {
				dropMu.Lock()
				fmt.Printf("Warning: Failed to drop table %s: %v\n", t, err)
				dropMu.Unlock()
			}
		}(table)
	}
	dropWg.Wait()

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

// getMigrationContent reads migration file with in-memory caching
func (m *Migrator) getMigrationContent(filePath string) ([]byte, error) {
	if m.fileCache == nil {
		m.fileCache = make(map[string][]byte)
	}
	if content, ok := m.fileCache[filePath]; ok {
		return content, nil
	}
	// Also check conflict utils cache
	if cached, ok := m.conflictUtils.GetCachedContent(filePath); ok {
		m.fileCache[filePath] = cached
		return cached, nil
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	m.fileCache[filePath] = content
	return content, nil
}

// applySingleMigrationSafely applies migration and records it in a single transaction
func (m *Migrator) applySingleMigrationSafely(ctx context.Context, migration types.Migration) error {
	content, err := m.getMigrationContent(migration.FilePath)
	if err != nil {
		return fmt.Errorf("failed to read migration file: %w", err)
	}

	checksum := utils.ComputeChecksum(content)

	// Extract only the UP section from the migration
	upSQL := extractUpSQL(string(content))

	// Use the combined method that does both operations in a single transaction
	if err := m.adapter.ExecuteAndRecordMigration(ctx, migration.ID, migration.Name, checksum, upSQL); err != nil {
		return err
	}

	return nil
}

// extractUpSQL extracts only the UP migration SQL from a migration file.
// Migration files may contain both -- +migrate Up and -- +migrate Down sections.
// The marker logic is intentionally strict: the line must start with "--" and
// contain "+migrate Up" (case-insensitive) to be recognized.
func extractUpSQL(content string) string {
	lines := strings.Split(content, "\n")
	var upLines []string
	inUpSection := false
	hasMarkers := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)

		// Strict marker detection: line must start with "--" and contain the migrate directive
		if strings.HasPrefix(trimmed, "--") && strings.Contains(lower, "+migrate up") {
			inUpSection = true
			hasMarkers = true
			continue
		}
		if strings.HasPrefix(trimmed, "--") && strings.Contains(lower, "+migrate down") {
			inUpSection = false
			continue
		}

		if inUpSection {
			upLines = append(upLines, line)
		}
	}

	// If no markers found, return entire content (legacy format)
	if !hasMarkers {
		return content
	}

	return strings.Join(upLines, "\n")
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
		Tables:    make(map[string]any),
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
