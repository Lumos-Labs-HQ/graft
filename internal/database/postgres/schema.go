package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

func (p *Adapter) GetCurrentSchema(ctx context.Context) ([]types.SchemaTable, error) {
	tables, err := p.PullCompleteSchema(ctx)
	if err != nil {
		return nil, err
	}

	// Fetch indexes for each table
	for i := range tables {
		indexes, err := p.GetTableIndexes(ctx, tables[i].Name)
		if err != nil {
			continue
		}
		tables[i].Indexes = indexes
	}

	return tables, nil
}

func (p *Adapter) GetCurrentEnums(ctx context.Context) ([]types.SchemaEnum, error) {
	query := `
		SELECT t.typname as enum_name, e.enumlabel as enum_value
		FROM pg_type t
		JOIN pg_enum e ON t.oid = e.enumtypid
		JOIN pg_namespace n ON t.typnamespace = n.oid
		WHERE t.typtype = 'e' AND n.nspname = 'public'
		ORDER BY t.typname, e.enumsortorder
	`

	rows, err := p.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	enumMap := make(map[string][]string)
	for rows.Next() {
		var enumName, enumValue string
		if err := rows.Scan(&enumName, &enumValue); err != nil {
			continue
		}
		enumMap[enumName] = append(enumMap[enumName], enumValue)
	}

	enums := make([]types.SchemaEnum, 0, len(enumMap))
	for name, values := range enumMap {
		enums = append(enums, types.SchemaEnum{Name: name, Values: values})
	}
	return enums, nil
}

func (p *Adapter) GetTableIndexes(ctx context.Context, tableName string) ([]types.SchemaIndex, error) {
	// Exclude indexes backed by PRIMARY KEY or UNIQUE constraints.
	// These cannot be dropped independently and cause errors like:
	// "cannot drop index users_pkey because constraint users_pkey on table users requires it"
	query := `
		SELECT 
			i.indexname,
			i.indexdef
		FROM pg_indexes i
		WHERE i.tablename = $1 AND i.schemaname = 'public'
		  AND i.indexname NOT IN (
			  SELECT c.relname
			  FROM pg_constraint pc
			  JOIN pg_class c ON c.oid = pc.conindid
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			  WHERE pc.contype IN ('p', 'u')
			    AND n.nspname = 'public'
		  )
	`

	rows, err := p.pool.Query(ctx, query, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var indexes []types.SchemaIndex
	for rows.Next() {
		var indexName, indexDef string
		if err := rows.Scan(&indexName, &indexDef); err != nil {
			continue
		}
		indexes = append(indexes, p.parseIndexDef(indexName, tableName, indexDef))
	}

	return indexes, nil
}

// parseIndexDef extracts structured index info from a PostgreSQL indexdef string.
func (p *Adapter) parseIndexDef(indexName, tableName, indexDef string) types.SchemaIndex {
	index := types.SchemaIndex{
		Name:  indexName,
		Table: tableName,
	}

	if strings.Contains(indexDef, "UNIQUE") {
		index.Unique = true
	}

	if _, after, ok := strings.Cut(indexDef, "("); ok {
		colsStr := after
		if endIdx := strings.LastIndex(colsStr, ")"); endIdx != -1 {
			colsStr = colsStr[:endIdx]
		}
		cols := strings.SplitSeq(colsStr, ",")
		for col := range cols {
			col = strings.TrimSpace(col)
			if before, ok := strings.CutSuffix(col, " DESC"); ok {
				col = before
			} else if before, ok := strings.CutSuffix(col, " ASC"); ok {
				col = before
			}
			if col != "" {
				index.Columns = append(index.Columns, col)
			}
		}
	}

	return index
}

func (p *Adapter) GetKeyspaces(ctx context.Context) ([]string, error) {
	return nil, nil
}

func (p *Adapter) GetAllTableNames(ctx context.Context) ([]string, error) {
	query := `
		SELECT tablename 
		FROM pg_tables 
		WHERE schemaname = 'public' 
			AND tablename NOT LIKE '_flash_%'
		ORDER BY tablename`

	rows, err := p.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		tables = append(tables, name)
	}
	return tables, nil
}

func (p *Adapter) GetAllTablesColumns(ctx context.Context, tableNames []string) (map[string][]types.SchemaColumn, error) {
	if len(tableNames) == 0 {
		return make(map[string][]types.SchemaColumn), nil
	}

	// Split the single large information_schema query into two simpler ones
	// and merge results in Go to reduce repeated scans.

	// Query 1: Get basic column info (fast, no joins)
	// Check both current_schema() and 'public' for robustness (handles branch schemas)
	columnsQuery := `
		SELECT DISTINCT ON (c.table_name, c.column_name)
			c.table_name,
			c.column_name,
			c.udt_name,
			c.is_nullable,
			c.column_default,
			c.character_maximum_length,
			c.numeric_precision,
			c.numeric_scale,
			c.ordinal_position
		FROM information_schema.columns c
		WHERE c.table_name = ANY($1)
		  AND c.table_schema IN (current_schema(), 'public')
		ORDER BY c.table_name, c.column_name, c.table_schema, c.ordinal_position
	`

	rows, err := p.pool.Query(ctx, columnsQuery, tableNames)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]types.SchemaColumn, len(tableNames))

	// First pass: collect all columns (let slices grow freely)
	for rows.Next() {
		var tableName string
		var column types.SchemaColumn
		var udtName, isNullable string
		var columnDefault sql.NullString
		var charMaxLength, numericPrecision, numericScale sql.NullInt64
		var ordinalPosition int

		err := rows.Scan(
			&tableName,
			&column.Name,
			&udtName,
			&isNullable,
			&columnDefault,
			&charMaxLength,
			&numericPrecision,
			&numericScale,
			&ordinalPosition,
		)
		if err != nil {
			return nil, err
		}

		column.Type = p.formatPostgresType(udtName, charMaxLength, numericPrecision, numericScale)
		column.Nullable = isNullable == "YES"

		if columnDefault.Valid {
			defaultStr := columnDefault.String
			column.IsAutoIncrement = strings.Contains(strings.ToLower(defaultStr), "nextval")
			column.Default = p.cleanDefaultValue(defaultStr)
		}

		result[tableName] = append(result[tableName], column)
	}

	// Build index AFTER all appends are done (slices won't reallocate anymore)
	// This ensures pointers remain valid when we apply constraints
	columnIndex := make(map[string]map[string]*types.SchemaColumn, len(result))
	for tableName, columns := range result {
		columnIndex[tableName] = make(map[string]*types.SchemaColumn, len(columns))
		for i := range columns {
			columnIndex[tableName][columns[i].Name] = &result[tableName][i]
		}
	}

	// Query 2: Get all constraints (PK, UNIQUE, FK) using pg_constraint directly
	// Using UNNEST with ordinality for proper FK column matching
	constraintsQuery := `
		WITH fk_columns AS (
			SELECT
				con.oid as constraint_oid,
				src_table.relname AS table_name,
				src_attr.attname AS column_name,
				tgt_table.relname AS foreign_table_name,
				tgt_attr.attname AS foreign_column_name,
				CASE con.confdeltype
					WHEN 'a' THEN 'NO ACTION'
					WHEN 'r' THEN 'RESTRICT'
					WHEN 'c' THEN 'CASCADE'
					WHEN 'n' THEN 'SET NULL'
					WHEN 'd' THEN 'SET DEFAULT'
				END AS on_delete_action
			FROM pg_constraint con
			JOIN pg_class src_table ON con.conrelid = src_table.oid
			JOIN pg_namespace ns ON src_table.relnamespace = ns.oid
			CROSS JOIN LATERAL UNNEST(con.conkey, con.confkey) WITH ORDINALITY AS cols(src_col, tgt_col, ord)
			JOIN pg_attribute src_attr ON src_attr.attrelid = src_table.oid AND src_attr.attnum = cols.src_col
			JOIN pg_class tgt_table ON con.confrelid = tgt_table.oid
			JOIN pg_attribute tgt_attr ON tgt_attr.attrelid = tgt_table.oid AND tgt_attr.attnum = cols.tgt_col
			WHERE src_table.relname = ANY($1)
			  AND ns.nspname IN (current_schema(), 'public')
			  AND con.contype = 'f'
		),
		pk_uk_columns AS (
			SELECT
				con.oid as constraint_oid,
				src_table.relname AS table_name,
				src_attr.attname AS column_name,
				CASE con.contype WHEN 'p' THEN 'PRIMARY KEY' ELSE 'UNIQUE' END AS constraint_type
			FROM pg_constraint con
			JOIN pg_class src_table ON con.conrelid = src_table.oid
			JOIN pg_namespace ns ON src_table.relnamespace = ns.oid
			CROSS JOIN LATERAL UNNEST(con.conkey) AS cols(src_col)
			JOIN pg_attribute src_attr ON src_attr.attrelid = src_table.oid AND src_attr.attnum = cols.src_col
			WHERE src_table.relname = ANY($1)
			  AND ns.nspname IN (current_schema(), 'public')
			  AND con.contype IN ('p', 'u')
		)
		SELECT table_name, column_name, 'PK' as constraint_type, NULL as foreign_table_name, NULL as foreign_column_name, NULL as on_delete_action, NULL as check_expr
		FROM pk_uk_columns WHERE constraint_type = 'PRIMARY KEY'
		UNION ALL
		SELECT table_name, column_name, 'UQ' as constraint_type, NULL, NULL, NULL, NULL
		FROM pk_uk_columns WHERE constraint_type = 'UNIQUE'
		UNION ALL
		SELECT table_name, column_name, 'FK' as constraint_type, foreign_table_name, foreign_column_name, on_delete_action, NULL
		FROM fk_columns
		UNION ALL
		SELECT
			src_table.relname, src_attr.attname, 'CK',
			NULL, NULL, NULL,
			pg_get_constraintdef(con.oid, true)
		FROM pg_constraint con
		JOIN pg_class src_table ON con.conrelid = src_table.oid
		JOIN pg_namespace ns ON src_table.relnamespace = ns.oid
		CROSS JOIN LATERAL UNNEST(con.conkey) AS cols(src_col)
		JOIN pg_attribute src_attr ON src_attr.attrelid = src_table.oid AND src_attr.attnum = cols.src_col
		WHERE src_table.relname = ANY($1)
		  AND ns.nspname IN (current_schema(), 'public')
		  AND con.contype = 'c'
	`

	conRows, err := p.pool.Query(ctx, constraintsQuery, tableNames)
	if err != nil {
		return nil, err
	}
	defer conRows.Close()

	for conRows.Next() {
		var tableName, columnName, constraintType string
		var foreignTable, foreignColumn, onDeleteAction, checkExpr sql.NullString

		err := conRows.Scan(&tableName, &columnName, &constraintType, &foreignTable, &foreignColumn, &onDeleteAction, &checkExpr)
		if err != nil {
			continue
		}

		if colMap, exists := columnIndex[tableName]; exists {
			if col, exists := colMap[columnName]; exists {
				switch constraintType {
				case "PK":
					col.IsPrimary = true
				case "UQ":
					col.IsUnique = true
				case "FK":
					if foreignTable.Valid && foreignColumn.Valid {
						col.ForeignKeyTable = foreignTable.String
						col.ForeignKeyColumn = foreignColumn.String
						if onDeleteAction.Valid {
							col.OnDeleteAction = onDeleteAction.String
						}
					}
				case "CK":
					if checkExpr.Valid {
						// pg_get_constraintdef returns e.g. "CHECK ((age >= 18))"
						// Strip outer CHECK (...) wrapper to get just the expression
						col.Check = stripCheckWrapper(checkExpr.String)
					}
				}
			}
		}
	}

	return result, nil
}

func (p *Adapter) GetAllTablesIndexes(ctx context.Context, tableNames []string) (map[string][]types.SchemaIndex, error) {
	if len(tableNames) == 0 {
		return make(map[string][]types.SchemaIndex), nil
	}

	// Exclude indexes backed by PRIMARY KEY or UNIQUE constraints.
	// These cannot be dropped independently and cause errors like:
	// "cannot drop index users_pkey because constraint users_pkey on table users requires it"
	query := `
		SELECT i.indexname, i.tablename, i.indexdef
		FROM pg_indexes i
		WHERE i.tablename = ANY($1) AND i.schemaname = 'public'
		  AND i.indexname NOT IN (
			  SELECT c.relname
			  FROM pg_constraint pc
			  JOIN pg_class c ON c.oid = pc.conindid
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			  WHERE pc.contype IN ('p', 'u')
			    AND n.nspname = 'public'
		  )
	`

	rows, err := p.pool.Query(ctx, query, tableNames)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]types.SchemaIndex)
	for rows.Next() {
		var indexName, tableName, indexDef string
		if err := rows.Scan(&indexName, &tableName, &indexDef); err != nil {
			continue
		}

		index := types.SchemaIndex{
			Name:  indexName,
			Table: tableName,
		}

		if strings.Contains(indexDef, "UNIQUE") {
			index.Unique = true
		}

		if idx := strings.Index(indexDef, "("); idx != -1 {
			colsStr := indexDef[idx+1:]
			if endIdx := strings.LastIndex(colsStr, ")"); endIdx != -1 {
				colsStr = colsStr[:endIdx]
			}
			cols := strings.SplitSeq(colsStr, ",")
			for col := range cols {
				col = strings.TrimSpace(col)
				if before, ok := strings.CutSuffix(col, " DESC"); ok {
					col = before
				} else if before, ok := strings.CutSuffix(col, " ASC"); ok {
					col = before
				}
				if col != "" {
					index.Columns = append(index.Columns, col)
				}
			}
		}

		result[tableName] = append(result[tableName], index)
	}

	return result, nil
}

func (p *Adapter) GetTableColumns(ctx context.Context, tableName string) ([]types.SchemaColumn, error) {
	allColumns, err := p.GetAllTablesColumns(ctx, []string{tableName})
	if err != nil {
		return nil, err
	}
	return allColumns[tableName], nil
}

// PullCompleteSchema returns all tables and columns in the database.
// It uses a two-query approach (columns + constraints) for speed.
func (p *Adapter) PullCompleteSchema(ctx context.Context) ([]types.SchemaTable, error) {
	// Step 1: Get all table names
	tableNames, err := p.GetAllTableNames(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get table names: %w", err)
	}

	if len(tableNames) == 0 {
		return []types.SchemaTable{}, nil
	}

	// Step 2: Get basic column info (fast, no joins)
	columnsQuery := `
		SELECT DISTINCT ON (c.table_name, c.column_name)
			c.table_name,
			c.column_name,
			c.udt_name,
			c.is_nullable,
			c.column_default,
			c.character_maximum_length,
			c.numeric_precision,
			c.numeric_scale,
			c.ordinal_position
		FROM information_schema.columns c
		WHERE c.table_name = ANY($1)
		  AND c.table_schema IN (current_schema(), 'public')
		ORDER BY c.table_name, c.column_name, c.table_schema, c.ordinal_position
	`

	rows, err := p.pool.Query(ctx, columnsQuery, tableNames)
	if err != nil {
		return nil, fmt.Errorf("failed to query columns: %w", err)
	}
	defer rows.Close()

	tableMap := make(map[string]*types.SchemaTable)
	columnIndex := make(map[string]map[string]*types.SchemaColumn)

	for rows.Next() {
		var tableName, columnName, udtName, isNullable string
		var columnDefault sql.NullString
		var charMaxLength, numericPrecision, numericScale sql.NullInt64
		var ordinalPosition int

		err := rows.Scan(
			&tableName, &columnName, &udtName, &isNullable, &columnDefault,
			&charMaxLength, &numericPrecision, &numericScale, &ordinalPosition,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan column: %w", err)
		}

		if _, exists := tableMap[tableName]; !exists {
			tableMap[tableName] = &types.SchemaTable{
				Name:    tableName,
				Columns: []types.SchemaColumn{},
			}
			columnIndex[tableName] = make(map[string]*types.SchemaColumn)
		}

		columnType := p.formatPullColumnType(udtName, charMaxLength, numericPrecision, numericScale, columnDefault.String, false)

		column := types.SchemaColumn{
			Name:     columnName,
			Type:     columnType,
			Nullable: isNullable == "YES",
			Default:  p.formatDefaultValue(columnDefault.String),
		}

		tableMap[tableName].Columns = append(tableMap[tableName].Columns, column)
	}
	rows.Close()

	// Build columnIndex AFTER all appends — appending can reallocate the slice,
	for tName, tbl := range tableMap {
		columnIndex[tName] = make(map[string]*types.SchemaColumn, len(tbl.Columns))
		for i := range tbl.Columns {
			columnIndex[tName][tbl.Columns[i].Name] = &tbl.Columns[i]
		}
	}

	// Step 3: Get all constraints using pg_constraint directly (much faster than information_schema joins)
	constraintsQuery := `
		WITH fk_columns AS (
			SELECT
				con.oid as constraint_oid,
				src_table.relname AS table_name,
				src_attr.attname AS column_name,
				tgt_table.relname AS foreign_table_name,
				tgt_attr.attname AS foreign_column_name,
				CASE con.confdeltype
					WHEN 'a' THEN 'NO ACTION'
					WHEN 'r' THEN 'RESTRICT'
					WHEN 'c' THEN 'CASCADE'
					WHEN 'n' THEN 'SET NULL'
					WHEN 'd' THEN 'SET DEFAULT'
				END AS on_delete_action
			FROM pg_constraint con
			JOIN pg_class src_table ON con.conrelid = src_table.oid
			JOIN pg_namespace ns ON src_table.relnamespace = ns.oid
			CROSS JOIN LATERAL UNNEST(con.conkey, con.confkey) WITH ORDINALITY AS cols(src_col, tgt_col, ord)
			JOIN pg_attribute src_attr ON src_attr.attrelid = src_table.oid AND src_attr.attnum = cols.src_col
			JOIN pg_class tgt_table ON con.confrelid = tgt_table.oid
			JOIN pg_attribute tgt_attr ON tgt_attr.attrelid = tgt_table.oid AND tgt_attr.attnum = cols.tgt_col
			WHERE src_table.relname = ANY($1)
			  AND ns.nspname IN (current_schema(), 'public')
			  AND con.contype = 'f'
		),
		pk_uk_columns AS (
			SELECT
				con.oid as constraint_oid,
				src_table.relname AS table_name,
				src_attr.attname AS column_name,
				CASE con.contype WHEN 'p' THEN 'PRIMARY KEY' ELSE 'UNIQUE' END AS constraint_type
			FROM pg_constraint con
			JOIN pg_class src_table ON con.conrelid = src_table.oid
			JOIN pg_namespace ns ON src_table.relnamespace = ns.oid
			CROSS JOIN LATERAL UNNEST(con.conkey) AS cols(src_col)
			JOIN pg_attribute src_attr ON src_attr.attrelid = src_table.oid AND src_attr.attnum = cols.src_col
			WHERE src_table.relname = ANY($1)
			  AND ns.nspname IN (current_schema(), 'public')
			  AND con.contype IN ('p', 'u')
		)
		SELECT table_name, column_name, 'PK' as constraint_type, NULL as foreign_table_name, NULL as foreign_column_name, NULL as on_delete_action, NULL as check_expr
		FROM pk_uk_columns WHERE constraint_type = 'PRIMARY KEY'
		UNION ALL
		SELECT table_name, column_name, 'UQ' as constraint_type, NULL, NULL, NULL, NULL
		FROM pk_uk_columns WHERE constraint_type = 'UNIQUE'
		UNION ALL
		SELECT table_name, column_name, 'FK' as constraint_type, foreign_table_name, foreign_column_name, on_delete_action, NULL
		FROM fk_columns
		UNION ALL
		SELECT
			src_table.relname, src_attr.attname, 'CK',
			NULL, NULL, NULL,
			pg_get_constraintdef(con.oid, true)
		FROM pg_constraint con
		JOIN pg_class src_table ON con.conrelid = src_table.oid
		JOIN pg_namespace ns ON src_table.relnamespace = ns.oid
		CROSS JOIN LATERAL UNNEST(con.conkey) AS cols(src_col)
		JOIN pg_attribute src_attr ON src_attr.attrelid = src_table.oid AND src_attr.attnum = cols.src_col
		WHERE src_table.relname = ANY($1)
		  AND ns.nspname IN (current_schema(), 'public')
		  AND con.contype = 'c'
	`

	conRows, err := p.pool.Query(ctx, constraintsQuery, tableNames)
	if err != nil {
		return nil, fmt.Errorf("failed to query constraints: %w", err)
	}
	defer conRows.Close()

	for conRows.Next() {
		var tableName, columnName, constraintType string
		var foreignTable, foreignColumn, onDeleteAction, checkExpr sql.NullString

		err := conRows.Scan(&tableName, &columnName, &constraintType, &foreignTable, &foreignColumn, &onDeleteAction, &checkExpr)
		if err != nil {
			continue
		}

		if colMap, exists := columnIndex[tableName]; exists {
			if col, exists := colMap[columnName]; exists {
				switch constraintType {
				case "PK":
					col.IsPrimary = true
					switch p.reverseMapType(col.Type) {
					case "int4":
						col.Type = "SERIAL"
					case "int8":
						col.Type = "BIGSERIAL"
					case "int2":
						col.Type = "SMALLSERIAL"
					default:
						col.Type = p.formatPullColumnType(p.reverseMapType(col.Type), sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}, col.Default, true)
					}
				case "UQ":
					col.IsUnique = true
				case "FK":
					if foreignTable.Valid && foreignColumn.Valid {
						col.ForeignKeyTable = foreignTable.String
						col.ForeignKeyColumn = foreignColumn.String
						if onDeleteAction.Valid {
							col.OnDeleteAction = onDeleteAction.String
						}
					}
				case "CK":
					if checkExpr.Valid {
						col.Check = stripCheckWrapper(checkExpr.String)
					}
				}
			}
		}
	}

	// Convert map to slice
	tables := make([]types.SchemaTable, 0, len(tableMap))
	for _, table := range tableMap {
		tables = append(tables, *table)
	}

	return tables, nil
}

// reverseMapType is a helper to reverse the type mapping for SERIAL detection.
// It converts formatted types back to udt_name equivalents.
func (p *Adapter) reverseMapType(formattedType string) string {
	switch strings.ToUpper(formattedType) {
	case "INT", "INTEGER", "SERIAL":
		return "int4"
	case "BIGINT", "BIGSERIAL":
		return "int8"
	case "VARCHAR":
		return "varchar"
	case "CHAR":
		return "bpchar"
	case "TEXT":
		return "text"
	case "BOOLEAN":
		return "bool"
	case "TIMESTAMP":
		return "timestamp"
	case "TIMESTAMP WITH TIME ZONE":
		return "timestamptz"
	case "DATE":
		return "date"
	case "TIME":
		return "time"
	case "NUMERIC":
		return "numeric"
	default:
		return strings.ToLower(formattedType)
	}
}

func (p *Adapter) formatPostgresType(udtName string, charMaxLength, numericPrecision, numericScale sql.NullInt64) string {
	switch udtName {
	case "varchar", "character varying":
		if charMaxLength.Valid {
			return fmt.Sprintf("VARCHAR(%d)", charMaxLength.Int64)
		}
		return "VARCHAR"
	case "bpchar", "character":
		if charMaxLength.Valid {
			return fmt.Sprintf("CHAR(%d)", charMaxLength.Int64)
		}
		return "CHAR"
	case "numeric":
		if numericPrecision.Valid && numericScale.Valid {
			return fmt.Sprintf("NUMERIC(%d,%d)", numericPrecision.Int64, numericScale.Int64)
		} else if numericPrecision.Valid {
			return fmt.Sprintf("NUMERIC(%d)", numericPrecision.Int64)
		}
		return "NUMERIC"
	case "timestamptz":
		return "TIMESTAMP WITH TIME ZONE"
	case "timestamp":
		return "TIMESTAMP"
	default:
		if mapped, exists := typeMap[strings.ToLower(udtName)]; exists {
			return mapped
		}
		return udtName
	}
}

func (p *Adapter) formatPullColumnType(dataType string, charMaxLength, numericPrecision, numericScale sql.NullInt64, defaultValue string, isPrimary bool) string {
	switch dataType {
	case "int4", "integer":
		if isPrimary && strings.Contains(defaultValue, "nextval(") {
			return "SERIAL"
		}
		return "INT"
	case "int8", "bigint":
		if isPrimary && strings.Contains(defaultValue, "nextval(") {
			return "BIGSERIAL"
		}
		return "BIGINT"
	case "varchar", "character varying":
		if charMaxLength.Valid {
			return fmt.Sprintf("VARCHAR(%d)", charMaxLength.Int64)
		}
		return "VARCHAR(255)"
	case "bpchar", "character":
		if charMaxLength.Valid {
			return fmt.Sprintf("CHAR(%d)", charMaxLength.Int64)
		}
		return "CHAR"
	case "text":
		return "TEXT"
	case "bool", "boolean":
		return "BOOLEAN"
	case "timestamp":
		return "TIMESTAMP"
	case "timestamptz":
		return "TIMESTAMP WITH TIME ZONE"
	case "date":
		return "DATE"
	case "time":
		return "TIME"
	case "numeric":
		if numericPrecision.Valid && numericScale.Valid {
			return fmt.Sprintf("NUMERIC(%d,%d)", numericPrecision.Int64, numericScale.Int64)
		} else if numericPrecision.Valid {
			return fmt.Sprintf("NUMERIC(%d)", numericPrecision.Int64)
		}
		return "NUMERIC"
	default:
		return strings.ToUpper(dataType)
	}
}

func (p *Adapter) cleanDefaultValue(defaultVal string) string {
	if defaultVal == "" {
		return ""
	}

	if before, _, ok := strings.Cut(defaultVal, "::"); ok {
		value := strings.TrimSpace(before)

		if strings.Contains(strings.ToLower(value), "nextval") {
			return ""
		}

		if strings.Contains(strings.ToUpper(value), "NOW()") || strings.Contains(strings.ToUpper(value), "CURRENT_TIMESTAMP") {
			return "NOW()"
		}

		return value
	}

	if strings.Contains(strings.ToUpper(defaultVal), "NOW()") || strings.Contains(strings.ToUpper(defaultVal), "CURRENT_TIMESTAMP") {
		return "NOW()"
	}

	return defaultVal
}

func (p *Adapter) formatDefaultValue(defaultVal string) string {
	if defaultVal == "" {
		return ""
	}

	cleaned := p.cleanDefaultValue(defaultVal)
	if cleaned == "" {
		return ""
	}

	// Handle boolean defaults
	upper := strings.ToUpper(cleaned)
	if upper == "TRUE" || upper == "FALSE" {
		return upper
	}

	// Remove type casts for display
	if idx := strings.Index(cleaned, "::"); idx != -1 {
		cleaned = strings.TrimSpace(cleaned[:idx])
	}

	return cleaned
}

// stripCheckWrapper strips the "CHECK ((...))  " wrapper from pg_get_constraintdef output.
// e.g. "CHECK ((age >= 18))" → "age >= 18"
func stripCheckWrapper(expr string) string {
	expr = strings.TrimSpace(expr)
	upper := strings.ToUpper(expr)
	if strings.HasPrefix(upper, "CHECK") {
		expr = strings.TrimSpace(expr[5:])
	}
	// Strip outer parens (PostgreSQL wraps in double parens)
	for strings.HasPrefix(expr, "(") && strings.HasSuffix(expr, ")") {
		inner := expr[1 : len(expr)-1]
		// Make sure parens are balanced before stripping
		depth := 0
		balanced := true
		for _, c := range inner {
			if c == '(' {
				depth++
			} else if c == ')' {
				depth--
				if depth < 0 {
					balanced = false
					break
				}
			}
		}
		if balanced && depth == 0 {
			expr = strings.TrimSpace(inner)
		} else {
			break
		}
	}
	return expr
}
