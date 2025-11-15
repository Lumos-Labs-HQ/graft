package postgres

import (
	"context"
	"fmt"

	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

func (a *Adapter) CreateBranchSchema(ctx context.Context, branchName string) error {
	query := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", branchName)
	_, err := a.pool.Exec(ctx, query)
	return err
}

func (a *Adapter) DropBranchSchema(ctx context.Context, branchName string) error {
	query := fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", branchName)
	_, err := a.pool.Exec(ctx, query)
	return err
}

func (a *Adapter) CloneSchemaToBranch(ctx context.Context, sourceSchema, targetSchema string) error {
	if err := a.DropBranchSchema(ctx, targetSchema); err != nil {
		return fmt.Errorf("failed to drop existing schema: %w", err)
	}
	if err := a.CreateBranchSchema(ctx, targetSchema); err != nil {
		return err
	}

	tables, err := a.GetTableNamesInSchema(ctx, sourceSchema)
	if err != nil {
		return err
	}

	for _, table := range tables {
		// Skip the migrations table - it will be created by the migration system
		if table == "_flash_migrations" {
			continue
		}

		// Create table structure
		createQuery := fmt.Sprintf(
			"CREATE TABLE %s.%s (LIKE %s.%s INCLUDING ALL)",
			targetSchema, table, sourceSchema, table,
		)
		if _, err := a.pool.Exec(ctx, createQuery); err != nil {
			return fmt.Errorf("failed to create table %s: %w", table, err)
		}

		// Copy data in separate query
		insertQuery := fmt.Sprintf(
			"INSERT INTO %s.%s SELECT * FROM %s.%s",
			targetSchema, table, sourceSchema, table,
		)
		if _, err := a.pool.Exec(ctx, insertQuery); err != nil {
			return fmt.Errorf("failed to copy data for table %s: %w", table, err)
		}
	}

	return nil
}

func (a *Adapter) GetSchemaForBranch(ctx context.Context, branchSchema string) ([]types.SchemaTable, error) {
	query := `
		SELECT 
			t.table_name,
			c.column_name,
			c.data_type,
			c.is_nullable,
			c.column_default
		FROM information_schema.tables t
		JOIN information_schema.columns c ON t.table_name = c.table_name
		WHERE t.table_schema = $1 AND t.table_type = 'BASE TABLE'
		ORDER BY t.table_name, c.ordinal_position
	`

	rows, err := a.pool.Query(ctx, query, branchSchema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tablesMap := make(map[string]*types.SchemaTable)

	for rows.Next() {
		var tableName, columnName, dataType, isNullable string
		var columnDefault *string

		if err := rows.Scan(&tableName, &columnName, &dataType, &isNullable, &columnDefault); err != nil {
			return nil, err
		}

		if _, exists := tablesMap[tableName]; !exists {
			tablesMap[tableName] = &types.SchemaTable{
				Name:    tableName,
				Columns: []types.SchemaColumn{},
			}
		}

		column := types.SchemaColumn{
			Name:     columnName,
			Type:     dataType,
			Nullable: isNullable == "YES",
		}
		if columnDefault != nil {
			column.Default = *columnDefault
		}

		tablesMap[tableName].Columns = append(tablesMap[tableName].Columns, column)
	}

	tables := make([]types.SchemaTable, 0, len(tablesMap))
	for _, table := range tablesMap {
		tables = append(tables, *table)
	}

	return tables, nil
}

func (a *Adapter) SetActiveSchema(ctx context.Context, schemaName string) error {
	query := fmt.Sprintf("SET search_path TO %s", schemaName)
	_, err := a.pool.Exec(ctx, query)
	return err
}

func (a *Adapter) GetTableNamesInSchema(ctx context.Context, schemaName string) ([]string, error) {
	query := `
		SELECT table_name 
		FROM information_schema.tables 
		WHERE table_schema = $1 AND table_type = 'BASE TABLE'
		ORDER BY table_name
	`

	rows, err := a.pool.Query(ctx, query, schemaName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, err
		}
		tables = append(tables, tableName)
	}

	return tables, nil
}
