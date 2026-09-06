package migrator

import (
	"context"
	"fmt"
	"regexp"
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
	provider      string // Database provider: sqlite, postgresql, mysql
	force         bool
	fileUtils     *utils.FileUtils
	inputUtils    *utils.InputUtils
	conflictUtils *utils.ConflictUtils
	fileCache     map[string][]byte // In-memory cache for migration file contents
}

func NewMigrator(cfg *config.Config) (*Migrator, error) {
	return newMigratorInternal(cfg, false)
}

// NewMigratorForGenerate creates a Migrator for flash migrate — skips DB connection
// when a valid schema snapshot already exists, since generation only needs the snapshot + schema files.
func NewMigratorForGenerate(cfg *config.Config) (*Migrator, error) {
	snapshotPath := schema.SnapshotPath(cfg.MigrationsPath)
	snap, _ := schema.LoadSchemaSnapshot(snapshotPath)
	if snap != nil {
		// Snapshot exists — no DB needed for diff generation
		return newMigratorInternal(cfg, true)
	}
	return newMigratorInternal(cfg, false)
}

func newMigratorInternal(cfg *config.Config, skipConnect bool) (*Migrator, error) {
	adapter := database.NewAdapter(cfg.Database.Provider)

	if !skipConnect {
		dbURL, err := cfg.GetDatabaseURL()
		if err != nil {
			return nil, fmt.Errorf("failed to get database URL: %w", err)
		}
		if err := adapter.Connect(context.Background(), dbURL); err != nil {
			return nil, fmt.Errorf("failed to connect to database: %w", err)
		}
	}

	return &Migrator{
		adapter:       adapter,
		schemaManager: schema.NewSchemaManager(adapter),
		migrationsDir: cfg.MigrationsPath,
		schemaPath:    cfg.GetSchemaDir(),
		provider:      cfg.Database.Provider,
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
	// ScyllaDB/Cassandra has no NOT NULL constraint enforcement — skip conflict detection entirely.
	if m.provider == "scylla" || m.provider == "scylladb" || m.provider == "cassandra" {
		return false, nil, nil
	}

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

func (m *Migrator) askUserConfirmation(message string) bool {
	return m.inputUtils.AskConfirmation(message, m.force)
}

// extractRefTables extracts table names from a CREATE MATERIALIZED VIEW statement's SELECT clause.
func extractRefTables(viewSQL string) []string {
	upper := strings.ToUpper(viewSQL)
	var tables []string
	seen := map[string]bool{}
	fromRe := regexp.MustCompile(`(?i)\bFROM\s+(\S+)`)
	for _, m := range fromRe.FindAllStringSubmatch(viewSQL, -1) {
		name := strings.TrimSpace(m[1])
		if !seen[name] {
			seen[name] = true
			tables = append(tables, name)
		}
	}
	joinRe := regexp.MustCompile(`(?i)\bJOIN\s+(\S+)`)
	for _, m := range joinRe.FindAllStringSubmatch(upper, -1) {
		name := strings.TrimSpace(m[1])
		if !seen[name] {
			seen[name] = true
			tables = append(tables, name)
		}
	}
	return tables
}

// isTableInNewTables checks if a table is already being created in this migration.
func isTableInNewTables(name string, newTables []types.SchemaTable) bool {
	for _, t := range newTables {
		if strings.EqualFold(t.Name, name) {
			return true
		}
		if _, after, ok := strings.CutLast(t.Name, "."); ok && strings.EqualFold(after, name) {
			return true
		}
		if _, after, ok := strings.CutLast(name, "."); ok && strings.EqualFold(t.Name, after) {
			return true
		}
	}
	return false
}

// findTableInSchema finds a table from the schema files by name.
func findTableInSchema(name string, sm *schema.SchemaManager, schemaPath string) *types.SchemaTable {
	tables, _, _, _, _ := sm.ParseSchemaPathAll(schemaPath)
	for _, t := range tables {
		if strings.EqualFold(t.Name, name) {
			return &t
		}
		if _, after, ok := strings.CutLast(t.Name, "."); ok && strings.EqualFold(after, name) {
			return &t
		}
		if _, after, ok := strings.CutLast(name, "."); ok && strings.EqualFold(t.Name, after) {
			return &t
		}
	}
	return nil
}
