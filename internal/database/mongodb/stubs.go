package mongodb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Lumos-Labs-HQ/flash/internal/database/common"
	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

func (a *Adapter) CreateMigrationsTable(ctx context.Context) error             { return nil }
func (a *Adapter) EnsureMigrationTableCompatibility(ctx context.Context) error { return nil }
func (a *Adapter) CleanupBrokenMigrationRecords(ctx context.Context) error     { return nil }

func (a *Adapter) GetAppliedMigrations(ctx context.Context) (map[string]*time.Time, error) {
	return nil, nil
}

func (a *Adapter) RecordMigration(ctx context.Context, migrationID, name, checksum string) error {
	return nil
}

func (a *Adapter) RemoveMigrationRecord(ctx context.Context, migrationID string) error {
	return nil
}

func (a *Adapter) ExecuteMigration(ctx context.Context, migrationSQL string) error {
	return nil
}

func (a *Adapter) ExecuteAndRecordMigration(ctx context.Context, migrationID, name, checksum string, migrationSQL string) error {
	return nil
}

func (a *Adapter) ExecuteQuery(ctx context.Context, query string) (*common.QueryResult, error) {
	return nil, nil
}

func (a *Adapter) ExecuteQueryWithArgs(ctx context.Context, query string, args ...any) (*common.QueryResult, error) {
	return nil, fmt.Errorf("MongoDB does not support SQL queries")
}

func (a *Adapter) ExecuteDMLWithArgs(ctx context.Context, query string, args ...any) error {
	return fmt.Errorf("MongoDB does not support SQL DML")
}

func (a *Adapter) QuoteIdentifier(name string) string {
	return name
}

func (a *Adapter) ProviderName() string {
	return "mongodb"
}

func (a *Adapter) GetCurrentSchema(ctx context.Context) ([]types.SchemaTable, error) {
	return nil, nil
}

func (a *Adapter) GetCurrentEnums(ctx context.Context) ([]types.SchemaEnum, error) {
	return nil, nil
}

func (a *Adapter) GetTableIndexes(ctx context.Context, tableName string) ([]types.SchemaIndex, error) {
	return nil, nil
}

func (a *Adapter) PullCompleteSchema(ctx context.Context) ([]types.SchemaTable, error) {
	return nil, nil
}

func (a *Adapter) CheckTableExists(ctx context.Context, tableName string) (bool, error) {
	return false, nil
}

func (a *Adapter) CheckColumnExists(ctx context.Context, tableName, columnName string) (bool, error) {
	return false, nil
}

func (a *Adapter) CheckNotNullConstraint(ctx context.Context, tableName, columnName string) (bool, error) {
	return false, nil
}

func (a *Adapter) CheckForeignKeyConstraint(ctx context.Context, tableName, constraintName string) (bool, error) {
	return false, nil
}

func (a *Adapter) CheckUniqueConstraint(ctx context.Context, tableName, constraintName string) (bool, error) {
	return false, nil
}

func (a *Adapter) DropTable(ctx context.Context, tableName string) error {
	return nil
}

func (a *Adapter) DropEnum(ctx context.Context, enumName string) error {
	return nil
}

func (a *Adapter) GenerateCreateTableSQL(table types.SchemaTable) string {
	return ""
}

func (a *Adapter) GenerateAddColumnSQL(tableName string, column types.SchemaColumn) string {
	return ""
}

func (a *Adapter) GenerateDropColumnSQL(tableName, columnName string) string {
	return ""
}

func (a *Adapter) GenerateAlterColumnSQL(tableName string, column types.SchemaColumn, oldType string) string {
	return ""
}

func (a *Adapter) GenerateAddIndexSQL(index types.SchemaIndex) string {
	return ""
}

func (a *Adapter) GenerateDropIndexSQL(index types.SchemaIndex) string {
	return ""
}

func (a *Adapter) MapColumnType(dbType string) string {
	return "string"
}

func (a *Adapter) FormatColumnType(column types.SchemaColumn) string {
	return column.Type
}

func (a *Adapter) CreateBranchSchema(ctx context.Context, branchName string) error {
	return nil
}

func (a *Adapter) DropBranchSchema(ctx context.Context, branchName string) error {
	return nil
}

func (a *Adapter) CloneSchemaToBranch(ctx context.Context, sourceSchema, targetSchema string) error {
	return nil
}

func (a *Adapter) GetSchemaForBranch(ctx context.Context, branchSchema string) ([]types.SchemaTable, error) {
	return nil, nil
}

func (a *Adapter) SetActiveSchema(ctx context.Context, schemaName string) error {
	return nil
}

func (a *Adapter) GetTableNamesInSchema(ctx context.Context, schemaName string) ([]string, error) {
	return nil, nil
}

func (a *Adapter) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return nil, nil
}

func (a *Adapter) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return nil
}

func (a *Adapter) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return nil, nil
}

func (a *Adapter) Begin(ctx context.Context) (*sql.Tx, error) {
	return nil, nil
}
