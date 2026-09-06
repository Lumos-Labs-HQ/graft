package clickhouse

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/Lumos-Labs-HQ/flash/internal/database/common"
)

type Adapter struct {
	db *sql.DB
}

func New() *Adapter {
	return &Adapter{}
}

var typeMap = map[string]string{
	"int8": "INT8", "int16": "INT16", "int32": "INT32", "int64": "INT64",
	"uint8": "UINT8", "uint16": "UINT16", "uint32": "UINT32", "uint64": "UINT64",
	"float32": "FLOAT32", "float64": "FLOAT64",
	"string": "STRING", "fixedstring": "FIXEDSTRING",
	"date": "DATE", "date32": "DATE32",
	"datetime": "DATETIME", "datetime64": "DATETIME64",
	"uuid":    "UUID",
	"boolean": "BOOLEAN", "bool": "BOOLEAN",
	"decimal": "DECIMAL",
	"json":    "JSON",
}

func (a *Adapter) Connect(ctx context.Context, url string) error {
	opts, err := chdriver.ParseDSN(url)
	if err != nil {
		return fmt.Errorf("failed to parse ClickHouse DSN: %w", err)
	}
	db := chdriver.OpenDB(opts)
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("failed to connect to ClickHouse: %w", err)
	}
	a.db = db
	return nil
}

func (a *Adapter) Close() error {
	if a.db != nil {
		return a.db.Close()
	}
	return nil
}

func (a *Adapter) Ping(ctx context.Context) error {
	return a.db.PingContext(ctx)
}

func (a *Adapter) CreateMigrationsTable(ctx context.Context) error {
	_, err := a.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS _flash_migrations (
			id            String,
			migration_name String,
			checksum      String,
			started_at    DateTime DEFAULT now(),
			finished_at   Nullable(DateTime),
			applied_steps_count UInt32 DEFAULT 0
		) ENGINE = ReplacingMergeTree()
		ORDER BY id
	`)
	return err
}

func (a *Adapter) EnsureMigrationTableCompatibility(_ context.Context) error { return nil }

func (a *Adapter) CleanupBrokenMigrationRecords(ctx context.Context) error {
	_, err := a.db.ExecContext(ctx,
		`ALTER TABLE _flash_migrations DELETE WHERE finished_at IS NULL AND started_at < now() - INTERVAL 1 HOUR`)
	return err
}

func (a *Adapter) GetAppliedMigrations(ctx context.Context) (map[string]*time.Time, error) {
	rows, err := a.db.QueryContext(ctx,
		`SELECT id, finished_at FROM _flash_migrations WHERE finished_at IS NOT NULL`)
	if err != nil {
		if strings.Contains(err.Error(), "doesn't exist") || strings.Contains(err.Error(), "UNKNOWN_TABLE") {
			return make(map[string]*time.Time), nil
		}
		return nil, err
	}
	defer rows.Close()

	applied := make(map[string]*time.Time)
	for rows.Next() {
		var id string
		var finishedAt *time.Time
		if err := rows.Scan(&id, &finishedAt); err != nil {
			continue
		}
		applied[id] = finishedAt
	}
	return applied, rows.Err()
}

func (a *Adapter) RecordMigration(ctx context.Context, migrationID, name, checksum string) error {
	now := time.Now()
	_, err := a.db.ExecContext(ctx,
		`INSERT INTO _flash_migrations (id, migration_name, checksum, started_at, finished_at, applied_steps_count) VALUES (?, ?, ?, ?, ?, 1)`,
		migrationID, name, checksum, now, now)
	return err
}

func (a *Adapter) RemoveMigrationRecord(ctx context.Context, migrationID string) error {
	_, err := a.db.ExecContext(ctx,
		`ALTER TABLE _flash_migrations DELETE WHERE id = ?`, migrationID)
	return err
}

func (a *Adapter) ExecuteAndRecordMigration(ctx context.Context, migrationID, name, checksum, migrationSQL string) error {
	if migrationSQL != "" {
		if err := a.ExecuteMigration(ctx, migrationSQL); err != nil {
			return err
		}
	}
	return a.RecordMigration(ctx, migrationID, name, checksum)
}

func (a *Adapter) ExecuteMigration(ctx context.Context, migrationSQL string) error {
	for _, stmt := range common.ParseSQLStatements(migrationSQL) {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := a.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("clickhouse: failed to execute %q: %w", stmt, err)
		}
	}
	return nil
}

func (a *Adapter) ExecuteQuery(ctx context.Context, query string) (*common.QueryResult, error) {
	return a.ExecuteQueryWithArgs(ctx, query)
}

func (a *Adapter) ExecuteQueryWithArgs(ctx context.Context, query string, args ...any) (*common.QueryResult, error) {
	rows, err := a.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var results []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			row[c] = vals[i]
		}
		results = append(results, row)
	}
	return &common.QueryResult{Columns: cols, Rows: results}, rows.Err()
}

func (a *Adapter) ExecuteDMLWithArgs(ctx context.Context, query string, args ...any) error {
	_, err := a.db.ExecContext(ctx, query, args...)
	return err
}

func (a *Adapter) QuoteIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func (a *Adapter) ProviderName() string { return "clickhouse" }

func (a *Adapter) MapColumnType(dbType string) string {
	core := stripNullable(dbType)
	if mapped, ok := typeMap[strings.ToLower(core)]; ok {
		return mapped
	}
	return strings.ToUpper(core)
}

func stripNullable(t string) string {
	t = strings.TrimSpace(t)
	lower := strings.ToLower(t)
	if strings.HasPrefix(lower, "nullable(") && strings.HasSuffix(t, ")") {
		return t[9 : len(t)-1]
	}
	return t
}

func (a *Adapter) currentDatabase(ctx context.Context) string {
	var db string
	_ = a.db.QueryRowContext(ctx, "SELECT currentDatabase()").Scan(&db)
	if db == "" {
		db = "default"
	}
	return db
}
