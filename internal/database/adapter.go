package database

import (
	"context"
	"database/sql"
	"time"

	"github.com/Lumos-Labs-HQ/flash/internal/database/common"
	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

type DatabaseAdapter interface {
	Connect(ctx context.Context, url string) error
	Close() error
	Ping(ctx context.Context) error

	// Migration table management
	CreateMigrationsTable(ctx context.Context) error
	EnsureMigrationTableCompatibility(ctx context.Context) error
	CleanupBrokenMigrationRecords(ctx context.Context) error

	// Migration operations
	GetAppliedMigrations(ctx context.Context) (map[string]*time.Time, error)
	RecordMigration(ctx context.Context, migrationID, name, checksum string) error
	RemoveMigrationRecord(ctx context.Context, migrationID string) error
	ExecuteMigration(ctx context.Context, migrationSQL string) error
	ExecuteAndRecordMigration(ctx context.Context, migrationID, name, checksum string, migrationSQL string) error
	ExecuteQuery(ctx context.Context, query string) (*common.QueryResult, error)
	ExecuteQueryWithArgs(ctx context.Context, query string, args ...any) (*common.QueryResult, error)
	ExecuteDMLWithArgs(ctx context.Context, query string, args ...any) error
	QuoteIdentifier(name string) string
	ProviderName() string

	// Schema operations
	GetCurrentSchema(ctx context.Context) ([]types.SchemaTable, error)
	GetCurrentEnums(ctx context.Context) ([]types.SchemaEnum, error)
	GetTableColumns(ctx context.Context, tableName string) ([]types.SchemaColumn, error) // Compatibility - prefer batch versions
	GetTableIndexes(ctx context.Context, tableName string) ([]types.SchemaIndex, error)  // Compatibility - prefer batch versions
	GetAllTableNames(ctx context.Context) ([]string, error)
	GetKeyspaces(ctx context.Context) ([]string, error)
	PullCompleteSchema(ctx context.Context) ([]types.SchemaTable, error)

	// Conflict detection
	CheckTableExists(ctx context.Context, tableName string) (bool, error)
	CheckColumnExists(ctx context.Context, tableName, columnName string) (bool, error)
	CheckNotNullConstraint(ctx context.Context, tableName, columnName string) (bool, error)
	CheckForeignKeyConstraint(ctx context.Context, tableName, constraintName string) (bool, error)
	CheckUniqueConstraint(ctx context.Context, tableName, constraintName string) (bool, error)

	// Backup operations
	GetTableData(ctx context.Context, tableName string) ([]map[string]any, error)
	GetTableRowCount(ctx context.Context, tableName string) (int, error)
	GetAllTableRowCounts(ctx context.Context, tableNames []string) (map[string]int, error)
	DropTable(ctx context.Context, tableName string) error
	DropEnum(ctx context.Context, enumName string) error

	// SQL generation
	GenerateCreateTableSQL(table types.SchemaTable) string
	GenerateAddColumnSQL(tableName string, column types.SchemaColumn) string
	GenerateDropColumnSQL(tableName, columnName string) string
	GenerateAlterColumnSQL(tableName string, column types.SchemaColumn, oldType string) string
	GenerateAddIndexSQL(index types.SchemaIndex) string
	GenerateDropIndexSQL(index types.SchemaIndex) string

	// Data type mapping
	MapColumnType(dbType string) string
	FormatColumnType(column types.SchemaColumn) string

	// Branch operations
	CreateBranchSchema(ctx context.Context, branchName string) error
	DropBranchSchema(ctx context.Context, branchName string) error
	CloneSchemaToBranch(ctx context.Context, sourceSchema, targetSchema string) error
	GetSchemaForBranch(ctx context.Context, branchSchema string) ([]types.SchemaTable, error)
	SetActiveSchema(ctx context.Context, schemaName string) error
	GetTableNamesInSchema(ctx context.Context, schemaName string) ([]string, error)
}

type DatabaseConnection interface {
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, query string, args ...any) *sql.Row
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
	Begin(ctx context.Context) (*sql.Tx, error)
}
