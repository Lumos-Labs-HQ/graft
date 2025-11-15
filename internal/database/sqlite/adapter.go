package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Lumos-Labs-HQ/flash/internal/database/common"
	"github.com/Masterminds/squirrel"
	_ "github.com/mattn/go-sqlite3"
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
	if !strings.Contains(dbPath, "?") {
		dbPath += "?cache=shared&_journal_mode=WAL"
	}

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
	if !strings.Contains(dbPath, "?") {
		dbPath += "?cache=shared&_journal_mode=WAL"
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return fmt.Errorf("failed to switch to database %s: %w", branchFile, err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(5 * time.Minute)

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
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
	INSERT INTO _flash_migrations (id, migration_name, checksum, started_at, finished_at, applied_steps_count)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 1)
	`, migrationID, name, checksum)

	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Adapter) ExecuteAndRecordMigration(ctx context.Context, migrationID, name, checksum string, migrationSQL string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// First, record the migration with started_at only
	_, err = tx.ExecContext(ctx, `
		INSERT INTO _flash_migrations (id, migration_name, checksum, started_at, applied_steps_count)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, 0)
	`, migrationID, name, checksum)
	if err != nil {
		return fmt.Errorf("failed to record migration start: %w", err)
	}

	// Execute the migration SQL
	if migrationSQL != "" {
		statements := common.ParseSQLStatements(migrationSQL)
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

	return tx.Commit()
}

func (s *Adapter) ExecuteMigration(ctx context.Context, migrationSQL string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

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

	var results []map[string]interface{}
	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range columns {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		row := make(map[string]interface{})
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

func (s *Adapter) MapColumnType(dbType string) string {
	if mapped, exists := typeMap[strings.ToLower(dbType)]; exists {
		return mapped
	}
	return strings.ToUpper(dbType)
}
