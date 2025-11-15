package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

func (p *Adapter) GetCurrentSchema(ctx context.Context) ([]types.SchemaTable, error) {
	tableNames, err := p.GetAllTableNames(ctx)
	if err != nil {
		return nil, err
	}

	var validTables []string
	for _, name := range tableNames {
		if name != "_flash_migrations" {
			validTables = append(validTables, name)
		}
	}

	if len(validTables) == 0 {
		return []types.SchemaTable{}, nil
	}

	allColumns, err := p.GetAllTablesColumns(ctx, validTables)
	if err != nil {
		return nil, err
	}

	allIndexes, err := p.GetAllTablesIndexes(ctx, validTables)
	if err != nil {
		return nil, err
	}

	var tables []types.SchemaTable
	for _, name := range validTables {
		tables = append(tables, types.SchemaTable{
			Name:    name,
			Columns: allColumns[name],
			Indexes: allIndexes[name],
		})
	}
	return tables, nil
}

func (p *Adapter) GetCurrentEnums(ctx context.Context) ([]types.SchemaEnum, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT 
			t.typname as enum_name,
			e.enumlabel as enum_value
		FROM pg_type t
		JOIN pg_enum e ON t.oid = e.enumtypid
		JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
		WHERE n.nspname = 'public'
		ORDER BY t.typname, e.enumsortorder
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	enumMap := make(map[string][]string)
	for rows.Next() {
		var enumName, enumValue string
		if err := rows.Scan(&enumName, &enumValue); err != nil {
			return nil, err
		}
		enumMap[enumName] = append(enumMap[enumName], enumValue)
	}

	var enums []types.SchemaEnum
	for name, values := range enumMap {
		enums = append(enums, types.SchemaEnum{
			Name:   name,
			Values: values,
		})
	}

	return enums, nil
}

func (p *Adapter) GetAllTablesColumns(ctx context.Context, tableNames []string) (map[string][]types.SchemaColumn, error) {
	if len(tableNames) == 0 {
		return make(map[string][]types.SchemaColumn), nil
	}

	query := `
		SELECT 
			c.table_name,
			c.column_name, 
			c.udt_name, 
			c.is_nullable, 
			c.column_default,
			c.character_maximum_length, 
			c.numeric_precision, 
			c.numeric_scale,
			CASE WHEN pk.column_name IS NOT NULL THEN true ELSE false END as is_primary_key,
			CASE WHEN uq.column_name IS NOT NULL THEN true ELSE false END as is_unique,
			fk.foreign_table_name,
			fk.foreign_column_name,
			fk.on_delete_action,
			c.ordinal_position
		FROM information_schema.columns c
		LEFT JOIN (
			SELECT tc.table_name, ku.column_name
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage ku ON tc.constraint_name = ku.constraint_name
			WHERE tc.constraint_type = 'PRIMARY KEY'
		) pk ON c.table_name = pk.table_name AND c.column_name = pk.column_name
		LEFT JOIN (
			SELECT tc.table_name, ku.column_name
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage ku ON tc.constraint_name = ku.constraint_name
			WHERE tc.constraint_type = 'UNIQUE'
		) uq ON c.table_name = uq.table_name AND c.column_name = uq.column_name
		LEFT JOIN (
			SELECT 
				tc.table_name,
				kcu.column_name, 
				ccu.table_name AS foreign_table_name, 
				ccu.column_name AS foreign_column_name, 
				rc.delete_rule AS on_delete_action
			FROM information_schema.table_constraints AS tc
			JOIN information_schema.key_column_usage AS kcu ON tc.constraint_name = kcu.constraint_name
			JOIN information_schema.constraint_column_usage AS ccu ON ccu.constraint_name = tc.constraint_name
			JOIN information_schema.referential_constraints AS rc ON tc.constraint_name = rc.constraint_name
			WHERE tc.constraint_type = 'FOREIGN KEY'
		) fk ON c.table_name = fk.table_name AND c.column_name = fk.column_name
		WHERE c.table_name = ANY($1) AND c.table_schema = 'public'
		ORDER BY c.table_name, c.ordinal_position
	`

	rows, err := p.pool.Query(ctx, query, tableNames)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]types.SchemaColumn)
	for rows.Next() {
		var tableName string
		var column types.SchemaColumn
		var udtName, isNullable string
		var columnDefault sql.NullString
		var charMaxLength, numericPrecision, numericScale sql.NullInt64
		var isPrimary, isUnique bool
		var fkTable, fkColumn, onDelete sql.NullString
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
			&isPrimary,
			&isUnique,
			&fkTable,
			&fkColumn,
			&onDelete,
			&ordinalPosition,
		)
		if err != nil {
			return nil, err
		}

		column.Type = p.formatPostgresType(udtName, charMaxLength, numericPrecision, numericScale)
		column.Nullable = isNullable == "YES"
		column.IsPrimary = isPrimary
		column.IsUnique = isUnique

		if columnDefault.Valid {
			defaultStr := columnDefault.String
			column.IsAutoIncrement = strings.Contains(strings.ToLower(defaultStr), "nextval")
			column.Default = p.cleanDefaultValue(defaultStr)
		}

		if fkTable.Valid {
			column.ForeignKeyTable = fkTable.String
		}
		if fkColumn.Valid {
			column.ForeignKeyColumn = fkColumn.String
		}
		if onDelete.Valid {
			column.OnDeleteAction = onDelete.String
		}

		result[tableName] = append(result[tableName], column)
	}

	return result, nil
}

func (p *Adapter) GetAllTablesIndexes(ctx context.Context, tableNames []string) (map[string][]types.SchemaIndex, error) {
	if len(tableNames) == 0 {
		return make(map[string][]types.SchemaIndex), nil
	}

	query := `
		SELECT tablename, indexname, indexdef
		FROM pg_indexes
		WHERE tablename = ANY($1) AND schemaname = 'public' AND indexname NOT LIKE '%_pkey'
		ORDER BY tablename, indexname
	`

	rows, err := p.pool.Query(ctx, query, tableNames)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]types.SchemaIndex)
	for rows.Next() {
		var tableName, indexName, indexDef string
		if err := rows.Scan(&tableName, &indexName, &indexDef); err != nil {
			continue
		}

		index := types.SchemaIndex{
			Name:   indexName,
			Table:  tableName,
			Unique: strings.Contains(strings.ToUpper(indexDef), "UNIQUE"),
		}

		if start := strings.Index(indexDef, "("); start != -1 {
			if end := strings.Index(indexDef[start:], ")"); end != -1 {
				columnsStr := indexDef[start+1 : start+end]
				for _, col := range strings.Split(columnsStr, ",") {
					index.Columns = append(index.Columns, strings.TrimSpace(col))
				}
			}
		}

		result[tableName] = append(result[tableName], index)
	}

	return result, nil
}

func (p *Adapter) GetTableColumns(ctx context.Context, tableName string) ([]types.SchemaColumn, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT 
			c.column_name, c.udt_name, c.is_nullable, c.column_default,
			c.character_maximum_length, c.numeric_precision, c.numeric_scale,
			CASE WHEN pk.column_name IS NOT NULL THEN true ELSE false END as is_primary_key,
			CASE WHEN uq.column_name IS NOT NULL THEN true ELSE false END as is_unique,
			fk.foreign_table_name,
			fk.foreign_column_name,
			fk.on_delete_action
		FROM information_schema.columns c
		LEFT JOIN (
			SELECT ku.column_name
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage ku ON tc.constraint_name = ku.constraint_name
			WHERE tc.table_name = $1 AND tc.constraint_type = 'PRIMARY KEY'
		) pk ON c.column_name = pk.column_name
		LEFT JOIN (
			SELECT ku.column_name
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage ku ON tc.constraint_name = ku.constraint_name
			WHERE tc.table_name = $1 AND tc.constraint_type = 'UNIQUE'
		) uq ON c.column_name = uq.column_name
		LEFT JOIN (
			SELECT kcu.column_name, ccu.table_name AS foreign_table_name, ccu.column_name AS foreign_column_name, rc.delete_rule AS on_delete_action
			FROM information_schema.table_constraints AS tc
			JOIN information_schema.key_column_usage AS kcu ON tc.constraint_name = kcu.constraint_name
			JOIN information_schema.constraint_column_usage AS ccu ON ccu.constraint_name = tc.constraint_name
			JOIN information_schema.referential_constraints AS rc ON tc.constraint_name = rc.constraint_name
			WHERE tc.table_name = $1 AND tc.constraint_type = 'FOREIGN KEY'
		) fk ON c.column_name = fk.column_name
		WHERE c.table_name = $1 AND c.table_schema = 'public'
		ORDER BY c.ordinal_position
	`, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []types.SchemaColumn
	for rows.Next() {
		var column types.SchemaColumn
		var udtName, isNullable string
		var columnDefault sql.NullString
		var charMaxLength, numericPrecision, numericScale sql.NullInt64
		var isPrimary, isUnique bool
		var fkTable, fkColumn, onDelete sql.NullString

		err := rows.Scan(&column.Name, &udtName, &isNullable, &columnDefault,
			&charMaxLength, &numericPrecision, &numericScale, &isPrimary, &isUnique, &fkTable, &fkColumn, &onDelete)
		if err != nil {
			return nil, err
		}

		column.Type = p.formatPostgresType(udtName, charMaxLength, numericPrecision, numericScale)
		column.Nullable = isNullable == "YES"
		column.IsPrimary = isPrimary
		column.IsUnique = isUnique

		if columnDefault.Valid {
			defaultStr := columnDefault.String
			column.IsAutoIncrement = strings.Contains(strings.ToLower(defaultStr), "nextval")
			column.Default = p.cleanDefaultValue(defaultStr)
		}

		if fkTable.Valid {
			column.ForeignKeyTable = fkTable.String
		}
		if fkColumn.Valid {
			column.ForeignKeyColumn = fkColumn.String
		}
		if onDelete.Valid {
			column.OnDeleteAction = onDelete.String
		}

		columns = append(columns, column)
	}
	return columns, nil
}

func (p *Adapter) GetTableIndexes(ctx context.Context, tableName string) ([]types.SchemaIndex, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT indexname, indexdef
		FROM pg_indexes
		WHERE tablename = $1 AND schemaname = 'public' AND indexname NOT LIKE '%_pkey'
	`, tableName)
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

		index := types.SchemaIndex{
			Name:   indexName,
			Table:  tableName,
			Unique: strings.Contains(strings.ToUpper(indexDef), "UNIQUE"),
		}

		if start := strings.Index(indexDef, "("); start != -1 {
			if end := strings.Index(indexDef[start:], ")"); end != -1 {
				columnsStr := indexDef[start+1 : start+end]
				for _, col := range strings.Split(columnsStr, ",") {
					index.Columns = append(index.Columns, strings.TrimSpace(col))
				}
			}
		}
		indexes = append(indexes, index)
	}
	return indexes, nil
}

func (p *Adapter) GetAllTableNames(ctx context.Context) ([]string, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT table_name FROM information_schema.tables 
		WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'
		ORDER BY table_name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			continue
		}
		tables = append(tables, tableName)
	}
	return tables, nil
}

func (p *Adapter) PullCompleteSchema(ctx context.Context) ([]types.SchemaTable, error) {
	query := `
	SELECT 
		c.table_name,
		c.column_name,
		c.udt_name,
		c.is_nullable,
		c.column_default,
		c.character_maximum_length,
		c.numeric_precision,
		c.numeric_scale,
		c.ordinal_position,
		CASE WHEN pk.column_name IS NOT NULL THEN 'PRIMARY KEY' ELSE NULL END as is_primary,
		CASE WHEN uq.column_name IS NOT NULL THEN 'UNIQUE' ELSE NULL END as is_unique,
		fk.foreign_table_name,
		fk.foreign_column_name,
		fk.delete_rule
	FROM information_schema.columns c
	LEFT JOIN (
		SELECT kcu.table_name, kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu 
			ON tc.constraint_name = kcu.constraint_name 
			AND tc.table_schema = kcu.table_schema
		WHERE tc.constraint_type = 'PRIMARY KEY' AND tc.table_schema = 'public'
	) pk ON c.table_name = pk.table_name AND c.column_name = pk.column_name
	LEFT JOIN (
		SELECT kcu.table_name, kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu 
			ON tc.constraint_name = kcu.constraint_name 
			AND tc.table_schema = kcu.table_schema
		WHERE tc.constraint_type = 'UNIQUE' AND tc.table_schema = 'public'
	) uq ON c.table_name = uq.table_name AND c.column_name = uq.column_name
	LEFT JOIN (
		SELECT 
			kcu.table_name, 
			kcu.column_name,
			ccu.table_name AS foreign_table_name,
			ccu.column_name AS foreign_column_name,
			rc.delete_rule
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu 
			ON tc.constraint_name = kcu.constraint_name 
			AND tc.table_schema = kcu.table_schema
		JOIN information_schema.constraint_column_usage ccu 
			ON tc.constraint_name = ccu.constraint_name 
			AND tc.table_schema = ccu.table_schema
		JOIN information_schema.referential_constraints rc 
			ON tc.constraint_name = rc.constraint_name 
			AND tc.table_schema = rc.constraint_schema
		WHERE tc.constraint_type = 'FOREIGN KEY' AND tc.table_schema = 'public'
	) fk ON c.table_name = fk.table_name AND c.column_name = fk.column_name
	WHERE c.table_schema = 'public' 
		AND c.table_name NOT LIKE '_flash_%'
	ORDER BY c.table_name, c.ordinal_position`

	rows, err := p.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query schema: %w", err)
	}
	defer rows.Close()

	tableMap := make(map[string]*types.SchemaTable)
	columnsSeen := make(map[string]map[string]bool)

	for rows.Next() {
		var tableName, columnName, udtName, isNullable string
		var ordinalPosition int
		var columnDefault, isPrimary, isUnique, foreignTable, foreignColumn, deleteRule sql.NullString
		var charMaxLength, numericPrecision, numericScale sql.NullInt64

		err := rows.Scan(&tableName, &columnName, &udtName, &isNullable, &columnDefault,
			&charMaxLength, &numericPrecision, &numericScale, &ordinalPosition, &isPrimary, &isUnique,
			&foreignTable, &foreignColumn, &deleteRule)
		if err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		if _, exists := tableMap[tableName]; !exists {
			tableMap[tableName] = &types.SchemaTable{
				Name:    tableName,
				Columns: []types.SchemaColumn{},
			}
			columnsSeen[tableName] = make(map[string]bool)
		}

		if columnsSeen[tableName][columnName] {
			continue
		}
		columnsSeen[tableName][columnName] = true

		columnType := p.formatPullColumnType(udtName, charMaxLength, numericPrecision, numericScale, columnDefault.String, isPrimary.Valid)

		column := types.SchemaColumn{
			Name:      columnName,
			Type:      columnType,
			Nullable:  isNullable == "YES",
			Default:   p.formatDefaultValue(columnDefault.String),
			IsPrimary: isPrimary.Valid,
			IsUnique:  isUnique.Valid,
		}

		if foreignTable.Valid && foreignColumn.Valid {
			column.ForeignKeyTable = foreignTable.String
			column.ForeignKeyColumn = foreignColumn.String
			if deleteRule.Valid {
				column.OnDeleteAction = deleteRule.String
			}
		}

		tableMap[tableName].Columns = append(tableMap[tableName].Columns, column)
	}

	var tables []types.SchemaTable
	for _, table := range tableMap {
		tables = append(tables, *table)
	}

	return tables, nil
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

	if idx := strings.Index(defaultVal, "::"); idx != -1 {
		value := strings.TrimSpace(defaultVal[:idx])

		if strings.Contains(strings.ToLower(value), "nextval") {
			return ""
		}

		if strings.Contains(strings.ToUpper(value), "NOW()") || strings.Contains(strings.ToUpper(value), "CURRENT_TIMESTAMP") {
			return "NOW()"
		}

		return value
	}

	upper := strings.ToUpper(defaultVal)
	if strings.Contains(upper, "NEXTVAL") {
		return ""
	}
	if strings.Contains(upper, "NOW()") || strings.Contains(upper, "CURRENT_TIMESTAMP") {
		return "NOW()"
	}
	if upper == "TRUE" || upper == "FALSE" {
		return upper
	}

	return defaultVal
}

func (p *Adapter) formatDefaultValue(defaultValue string) string {
	if defaultValue == "" {
		return ""
	}

	if strings.Contains(defaultValue, "nextval(") {
		return ""
	}

	if strings.Contains(strings.ToLower(defaultValue), "now()") {
		return "NOW()"
	}

	return defaultValue
}
