package mysql

import (
	"context"
	"fmt"

	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

func (a *Adapter) CreateBranchSchema(ctx context.Context, branchName string) error {
	query := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", branchName)
	_, err := a.ExecuteQuery(ctx, query)
	return err
}

func (a *Adapter) DropBranchSchema(ctx context.Context, branchName string) error {
	query := fmt.Sprintf("DROP DATABASE IF EXISTS %s", branchName)
	_, err := a.ExecuteQuery(ctx, query)
	return err
}

func (a *Adapter) CloneSchemaToBranch(ctx context.Context, sourceSchema, targetSchema string) error {
	if err := a.CreateBranchSchema(ctx, targetSchema); err != nil {
		return err
	}

	tables, err := a.GetTableNamesInSchema(ctx, sourceSchema)
	if err != nil {
		return fmt.Errorf("failed to get tables from schema %s: %w", sourceSchema, err)
	}

	for _, table := range tables {
		// Create table structure
		createQuery := fmt.Sprintf("CREATE TABLE `%s`.`%s` LIKE `%s`.`%s`", targetSchema, table, sourceSchema, table)
		if _, err := a.db.ExecContext(ctx, createQuery); err != nil {
			return fmt.Errorf("failed to create table %s: %w", table, err)
		}

		// Copy data
		insertQuery := fmt.Sprintf("INSERT INTO `%s`.`%s` SELECT * FROM `%s`.`%s`", targetSchema, table, sourceSchema, table)
		if _, err := a.db.ExecContext(ctx, insertQuery); err != nil {
			return fmt.Errorf("failed to copy data for table %s: %w", table, err)
		}
	}

	return nil
}

func (a *Adapter) GetSchemaForBranch(ctx context.Context, branchSchema string) ([]types.SchemaTable, error) {
	query := `
		SELECT 
			t.TABLE_NAME,
			c.COLUMN_NAME,
			c.DATA_TYPE,
			c.IS_NULLABLE,
			c.COLUMN_DEFAULT
		FROM information_schema.TABLES t
		JOIN information_schema.COLUMNS c ON t.TABLE_NAME = c.TABLE_NAME AND t.TABLE_SCHEMA = c.TABLE_SCHEMA
		WHERE t.TABLE_SCHEMA = ? AND t.TABLE_TYPE = 'BASE TABLE'
		ORDER BY t.TABLE_NAME, c.ORDINAL_POSITION
	`

	rows, err := a.db.QueryContext(ctx, query, branchSchema)
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
	query := fmt.Sprintf("USE %s", schemaName)
	_, err := a.ExecuteQuery(ctx, query)
	return err
}

func (a *Adapter) GetTableNamesInSchema(ctx context.Context, schemaName string) ([]string, error) {
	query := `
		SELECT TABLE_NAME 
		FROM information_schema.TABLES 
		WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'BASE TABLE'
		ORDER BY TABLE_NAME
	`

	rows, err := a.db.QueryContext(ctx, query, schemaName)
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
