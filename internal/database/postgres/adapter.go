package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Lumos-Labs-HQ/flash/internal/database/common"
	"github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/lib/pq"
)

type Adapter struct {
	pool *pgxpool.Pool
	qb   squirrel.StatementBuilderType
}

var typeMap = map[string]string{
	"character varying": "VARCHAR", "varchar": "VARCHAR",
	"character": "CHAR", "char": "CHAR", "text": "TEXT",
	"integer": "INTEGER", "int4": "INTEGER", "bigint": "BIGINT", "int8": "BIGINT",
	"smallint": "SMALLINT", "int2": "SMALLINT", "boolean": "BOOLEAN", "bool": "BOOLEAN",
	"timestamp with time zone": "TIMESTAMP WITH TIME ZONE", "timestamptz": "TIMESTAMP WITH TIME ZONE",
	"timestamp without time zone": "TIMESTAMP", "timestamp": "TIMESTAMP",
	"date": "DATE", "time": "TIME", "numeric": "NUMERIC", "decimal": "NUMERIC",
	"real": "REAL", "float4": "REAL", "double precision": "DOUBLE PRECISION", "float8": "DOUBLE PRECISION",
	"uuid": "UUID", "json": "JSON", "jsonb": "JSONB",
}

func New() *Adapter {
	return &Adapter{
		qb: squirrel.StatementBuilder.PlaceholderFormat(squirrel.Dollar),
	}
}

func (p *Adapter) Connect(ctx context.Context, url string) error {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return fmt.Errorf("failed to parse connection URL: %w", err)
	}

	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec

	config.MaxConns = 2                         
	config.MinConns = 0                         
	config.MaxConnLifetime = 15 * time.Minute   
	config.MaxConnIdleTime = 3 * time.Minute    
	config.HealthCheckPeriod = 30 * time.Second 

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to create connection pool: %w", err)
	}

	p.pool = pool
	return nil
}

func (p *Adapter) Close() error {
	if p.pool != nil {
		p.pool.Close()
	}
	return nil
}

func (p *Adapter) Ping(ctx context.Context) error {
	return p.pool.Ping(ctx)
}

func (p *Adapter) CreateMigrationsTable(ctx context.Context) error {
	query := `CREATE TABLE IF NOT EXISTS _flash_migrations (
		id VARCHAR(255) PRIMARY KEY,
		checksum VARCHAR(64) NOT NULL,
		finished_at TIMESTAMP WITH TIME ZONE,
		migration_name VARCHAR(255) NOT NULL,
		logs TEXT,
		rolled_back_at TIMESTAMP WITH TIME ZONE,
		started_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
		applied_steps_count INTEGER NOT NULL DEFAULT 0
	)`
	_, err := p.pool.Exec(ctx, query)
	return err
}

func (p *Adapter) EnsureMigrationTableCompatibility(ctx context.Context) error {
	exists, err := p.columnExists("_flash_migrations", "logs")
	if err != nil {
		return err
	}
	if !exists {
		_, err = p.pool.Exec(ctx, "ALTER TABLE _flash_migrations ADD COLUMN logs TEXT")
	}
	return err
}

func (p *Adapter) CleanupBrokenMigrationRecords(ctx context.Context) error {
	_, err := p.pool.Exec(ctx, `
	DELETE FROM _flash_migrations 
		WHERE finished_at IS NULL AND started_at < NOW() - INTERVAL '1 hour'
	`)
	return err
}

func (p *Adapter) GetAppliedMigrations(ctx context.Context) (map[string]*time.Time, error) {
	applied := make(map[string]*time.Time)

	rows, err := p.pool.Query(ctx, `
		SELECT id, finished_at 
	FROM _flash_migrations 
		WHERE finished_at IS NOT NULL 
		ORDER BY started_at
	`)
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

func (p *Adapter) RecordMigration(ctx context.Context, migrationID, name, checksum string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
	INSERT INTO _flash_migrations (id, migration_name, checksum, started_at, finished_at, applied_steps_count)
		VALUES ($1, $2, $3, NOW(), NOW(), 1)
	`, migrationID, name, checksum)

	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Adapter) ExecuteAndRecordMigration(ctx context.Context, migrationID, name, checksum string, migrationSQL string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO _flash_migrations (id, migration_name, checksum, started_at, applied_steps_count)
		VALUES ($1, $2, $3, NOW(), 0)
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
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("failed to execute statement %d: %w", i+1, err)
			}
		}
	}

	// Update the migration record with finished_at
	_, err = tx.Exec(ctx, `
		UPDATE _flash_migrations 
		SET finished_at = NOW(), applied_steps_count = 1
		WHERE id = $1
	`, migrationID)
	if err != nil {
		return fmt.Errorf("failed to update migration finish time: %w", err)
	}

	return tx.Commit(ctx)
}

func (p *Adapter) ExecuteMigration(ctx context.Context, migrationSQL string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	statements := common.ParseSQLStatements(migrationSQL)

	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}

		_, err := tx.Exec(ctx, stmt)
		if err != nil {
			return fmt.Errorf("failed to execute statement '%s': %w", stmt, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit migration transaction: %w", err)
	}

	return nil
}

func (p *Adapter) ExecuteQuery(ctx context.Context, query string) (*common.QueryResult, error) {
	rows, err := p.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to execute query: %w", err)
	}
	defer rows.Close()

	fieldDescriptions := rows.FieldDescriptions()
	columns := make([]string, len(fieldDescriptions))
	for i, fd := range fieldDescriptions {
		columns[i] = string(fd.Name)
	}

	var results []map[string]interface{}
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		row := make(map[string]interface{})
		for i, col := range columns {
			row[col] = values[i]
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

func (p *Adapter) MapColumnType(dbType string) string {
	if mapped, exists := typeMap[strings.ToLower(dbType)]; exists {
		return mapped
	}
	return strings.ToUpper(dbType)
}
