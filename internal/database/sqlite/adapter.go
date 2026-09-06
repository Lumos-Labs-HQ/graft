package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Masterminds/squirrel"
	_ "github.com/mattn/go-sqlite3"

	"github.com/Lumos-Labs-HQ/flash/internal/database/common"
)

type Adapter struct {
	db           *sql.DB
	qb           squirrel.StatementBuilderType
	originalPath string
	currentPath  string
}

var typeMap = map[string]string{
	"varchar": "TEXT", "text": "TEXT", "char": "TEXT",
	"int": "INTEGER", "integer": "INTEGER", "bigint": "INTEGER", "smallint": "INTEGER", "tinyint": "INTEGER",
	"real": "REAL", "double": "REAL", "float": "REAL",
	"blob": "BLOB", "numeric": "NUMERIC", "decimal": "NUMERIC",
	"boolean": "INTEGER", "bool": "INTEGER",
	"date": "TEXT", "datetime": "TEXT", "timestamp": "TEXT",
}

func New() *Adapter {
	return &Adapter{
		qb: squirrel.StatementBuilder.PlaceholderFormat(squirrel.Question),
	}
}

func (s *Adapter) Connect(ctx context.Context, url string) error {
	dbPath := strings.TrimPrefix(url, "sqlite://")

	// Store original path without query parameters
	s.originalPath = strings.TrimPrefix(url, "sqlite://")
	if idx := strings.Index(s.originalPath, "?"); idx > 0 {
		s.originalPath = s.originalPath[:idx]
	}
	s.currentPath = s.originalPath

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open SQLite connection: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(5 * time.Minute)

	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("failed to configure SQLite busy_timeout: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		// Non-fatal for readonly filesystems or databases that cannot switch mode.
		_ = err
	}

	s.db = db
	return nil
}

func (s *Adapter) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *Adapter) SwitchDatabase(ctx context.Context, branchFile string) error {
	if s.currentPath == branchFile {
		return nil // Already on this file
	}

	// Close existing connection
	if s.db != nil {
		s.db.Close()
	}

	// Open new database file
	dbPath := branchFile

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return fmt.Errorf("failed to switch to database %s: %w", branchFile, err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(5 * time.Minute)

	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("failed to configure SQLite busy_timeout: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		// Non-fatal for readonly filesystems or databases that cannot switch mode.
		_ = err
	}

	s.db = db
	s.currentPath = branchFile
	return nil
}

func (s *Adapter) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Adapter) CreateMigrationsTable(ctx context.Context) error {
	query := `CREATE TABLE IF NOT EXISTS _flash_migrations (
		id TEXT PRIMARY KEY,
		checksum TEXT NOT NULL,
		finished_at TIMESTAMP,
		migration_name TEXT NOT NULL,
		logs TEXT,
		rolled_back_at TIMESTAMP,
		started_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		applied_steps_count INTEGER NOT NULL DEFAULT 0
	)`
	_, err := s.db.ExecContext(ctx, query)
	return err
}

func (s *Adapter) EnsureMigrationTableCompatibility(ctx context.Context) error {
	exists, err := s.columnExists("_flash_migrations", "logs")
	if err != nil {
		return err
	}
	if !exists {
		_, err = s.db.ExecContext(ctx, "ALTER TABLE _flash_migrations ADD COLUMN logs TEXT")
	}
	return err
}

func (s *Adapter) CleanupBrokenMigrationRecords(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM _flash_migrations WHERE finished_at IS NULL AND started_at < datetime('now', '-1 hour')")
	return err
}

func (s *Adapter) GetAppliedMigrations(ctx context.Context) (map[string]*time.Time, error) {
	applied := make(map[string]*time.Time)
	query := s.qb.Select("id", "finished_at").From("_flash_migrations").
		Where(squirrel.NotEq{"finished_at": nil}).OrderBy("started_at")

	sql, args, err := query.ToSql()
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, sql, args...)
	if err != nil {
		// If the migrations table doesn't exist yet, treat it as "no migrations applied".
		// This handles fresh databases where _flash_migrations hasn't been created.
		if strings.Contains(err.Error(), "no such table") {
			return applied, nil
		}
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		var finishedAt *time.Time
		if err := rows.Scan(&id, &finishedAt); err != nil {
			continue
		}
		applied[id] = finishedAt
	}
	return applied, nil
}

func (s *Adapter) RecordMigration(ctx context.Context, migrationID, name, checksum string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
	INSERT INTO _flash_migrations (id, migration_name, checksum, started_at, finished_at, applied_steps_count)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 1)
	`, migrationID, name, checksum)

	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Adapter) RemoveMigrationRecord(ctx context.Context, migrationID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM _flash_migrations WHERE id = ?", migrationID)
	return err
}

func (s *Adapter) ExecuteAndRecordMigration(ctx context.Context, migrationID, name, checksum string, migrationSQL string) error {
	// SQLite does not allow PRAGMA foreign_keys inside a transaction.
	// Detect and extract PRAGMA foreign_keys statements to run outside the transaction.
	pragmaOff, pragmaOn, cleanedSQL := extractForeignKeyPragmas(migrationSQL)

	// Execute PRAGMA foreign_keys=OFF before the transaction if present
	if pragmaOff != "" {
		if _, err := s.db.ExecContext(ctx, pragmaOff); err != nil {
			return fmt.Errorf("failed to execute PRAGMA foreign_keys=OFF: %w", err)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// First, record the migration with started_at only
	_, err = tx.ExecContext(ctx, `
		INSERT INTO _flash_migrations (id, migration_name, checksum, started_at, applied_steps_count)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, 0)
	`, migrationID, name, checksum)
	if err != nil {
		return fmt.Errorf("failed to record migration start: %w", err)
	}

	// Execute the migration SQL (without PRAGMA statements)
	if cleanedSQL != "" {
		statements := common.ParseSQLStatements(cleanedSQL)
		for i, stmt := range statements {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("failed to execute statement %d: %w", i+1, err)
			}
		}
	}

	// Update the migration record with finished_at
	_, err = tx.ExecContext(ctx, `
		UPDATE _flash_migrations 
		SET finished_at = CURRENT_TIMESTAMP, applied_steps_count = 1
		WHERE id = ?
	`, migrationID)
	if err != nil {
		return fmt.Errorf("failed to update migration finish time: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// Execute PRAGMA foreign_keys=ON after the transaction if present
	if pragmaOn != "" {
		if _, err := s.db.ExecContext(ctx, pragmaOn); err != nil {
			return fmt.Errorf("failed to execute PRAGMA foreign_keys=ON: %w", err)
		}
	}

	return nil
}

// extractForeignKeyPragmas scans migration SQL for PRAGMA foreign_keys statements
// and returns them separately from the cleaned SQL.
func extractForeignKeyPragmas(sql string) (off string, on string, cleaned string) {
	lines := strings.Split(sql, "\n")
	var cleanedLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.ToLower(line))
		if strings.HasPrefix(trimmed, "pragma foreign_keys=off") || strings.HasPrefix(trimmed, "pragma foreign_keys = off") {
			off = strings.TrimSpace(line)
			continue
		}
		if strings.HasPrefix(trimmed, "pragma foreign_keys=on") || strings.HasPrefix(trimmed, "pragma foreign_keys = on") {
			on = strings.TrimSpace(line)
			continue
		}
		cleanedLines = append(cleanedLines, line)
	}
	return off, on, strings.Join(cleanedLines, "\n")
}

func (s *Adapter) ExecuteMigration(ctx context.Context, migrationSQL string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	statements := common.ParseSQLStatements(migrationSQL)

	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}

		_, err := tx.ExecContext(ctx, stmt)
		if err != nil {
			return fmt.Errorf("failed to execute statement '%s': %w", stmt, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit migration transaction: %w", err)
	}

	return nil
}

func (s *Adapter) ExecuteQuery(ctx context.Context, query string) (*common.QueryResult, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to execute query: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to get columns: %w", err)
	}

	var results []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		valuePtrs := make([]any, len(columns))
		for i := range columns {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		row := make(map[string]any)
		for i, col := range columns {
			val := values[i]
			if b, ok := val.([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = val
			}
		}
		results = append(results, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	return &common.QueryResult{
		Columns: columns,
		Rows:    results,
	}, nil
}

func (s *Adapter) ExecuteQueryWithArgs(ctx context.Context, query string, args ...any) (*common.QueryResult, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute query: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to get columns: %w", err)
	}

	var results []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		valuePtrs := make([]any, len(columns))
		for i := range columns {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		row := make(map[string]any)
		for i, col := range columns {
			val := values[i]
			if b, ok := val.([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = val
			}
		}
		results = append(results, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	return &common.QueryResult{
		Columns: columns,
		Rows:    results,
	}, nil
}

func (s *Adapter) ExecuteDMLWithArgs(ctx context.Context, query string, args ...any) error {
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

func (s *Adapter) QuoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func (s *Adapter) ProviderName() string {
	return "sqlite"
}

func (s *Adapter) MapColumnType(dbType string) string {
	dbType = strings.ToLower(strings.TrimSpace(dbType))
	// Strip size/precision parameters like (255), (10,2) for lookup
	if idx := strings.Index(dbType, "("); idx > 0 {
		dbType = dbType[:idx]
	}
	if mapped, exists := typeMap[dbType]; exists {
		return mapped
	}
	return strings.ToUpper(dbType)
}
