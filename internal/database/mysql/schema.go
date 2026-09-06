package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

func (m *Adapter) GetCurrentSchema(ctx context.Context) ([]types.SchemaTable, error) {
	tableNames, err := m.GetAllTableNames(ctx)
	if err != nil {
		return nil, err
	}

	validTables := make([]string, 0, len(tableNames))
	for _, name := range tableNames {
		if name != "_flash_migrations" {
			validTables = append(validTables, name)
		}
	}

	if len(validTables) == 0 {
		return []types.SchemaTable{}, nil
	}

	allColumns, err := m.GetAllTablesColumns(ctx, validTables)
	if err != nil {
		return nil, err
	}

	allIndexes, err := m.GetAllTablesIndexes(ctx, validTables)
	if err != nil {
		return nil, err
	}

	tables := make([]types.SchemaTable, 0, len(validTables))
	for _, name := range validTables {
		tables = append(tables, types.SchemaTable{
			Name:    name,
			Columns: allColumns[name],
			Indexes: allIndexes[name],
		})
	}
	return tables, nil
}

func (m *Adapter) GetCurrentEnums(ctx context.Context) ([]types.SchemaEnum, error) {
	query := `
		SELECT DISTINCT
			CONCAT(table_name, '$', column_name) as enum_name,
			column_type
		FROM information_schema.columns
		WHERE table_schema = DATABASE()
		AND data_type = 'enum'
		AND table_name NOT LIKE '_flash_%'
		ORDER BY table_name, column_name
	`

	rows, err := m.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	enums := make([]types.SchemaEnum, 0, 8)
	for rows.Next() {
		var enumName, columnType string
		if err := rows.Scan(&enumName, &columnType); err != nil {
			return nil, err
		}

		values := extractEnumValues(columnType)
		if len(values) > 0 {
			enums = append(enums, types.SchemaEnum{
				Name:   enumName,
				Values: values,
			})
		}
	}

	return enums, nil
}

func extractEnumValues(columnType string) []string {
	if !strings.HasPrefix(columnType, "enum(") {
		return nil
	}

	values := strings.TrimPrefix(columnType, "enum(")
	values = strings.TrimSuffix(values, ")")

	parts := strings.Split(values, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, "'\"")
		if part != "" {
			result = append(result, part)
		}
	}

	return result
}

func (m *Adapter) GetAllTablesColumns(ctx context.Context, tableNames []string) (map[string][]types.SchemaColumn, error) {
	if len(tableNames) == 0 {
		return make(map[string][]types.SchemaColumn), nil
	}

	placeholders := make([]string, len(tableNames))
	args := make([]any, len(tableNames))
	for i, name := range tableNames {
		placeholders[i] = "?"
		args[i] = name
	}

	query := fmt.Sprintf(`
		SELECT 
			c.table_name,
			c.column_name, 
			c.data_type, 
			c.is_nullable, 
			c.column_default,
			c.character_maximum_length, 
			c.numeric_precision, 
			c.numeric_scale, 
			c.column_type,
			CASE WHEN c.column_key = 'PRI' THEN 1 ELSE 0 END as is_primary_key,
			CASE WHEN c.column_key = 'UNI' THEN 1 ELSE 0 END as is_unique,
			c.extra,
			c.ordinal_position,
			k.REFERENCED_TABLE_NAME,
			k.REFERENCED_COLUMN_NAME,
			r.DELETE_RULE,
			r.UPDATE_RULE
		FROM information_schema.columns c
		LEFT JOIN information_schema.key_column_usage k
			ON c.table_schema = k.table_schema
			AND c.table_name = k.table_name
			AND c.column_name = k.column_name
			AND k.referenced_table_name IS NOT NULL
		LEFT JOIN information_schema.referential_constraints r
			ON k.constraint_name = r.constraint_name
			AND k.table_schema = r.constraint_schema
		WHERE c.table_name IN (%s) AND c.table_schema = DATABASE()
		ORDER BY c.table_name, c.ordinal_position
	`, strings.Join(placeholders, ","))

	rows, err := m.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]types.SchemaColumn, len(tableNames))
	for rows.Next() {
		var tableName string
		var column types.SchemaColumn
		var dataType, isNullable, columnType, extra string
		var columnDefault, referencedTable, referencedColumn, onDeleteAction, onUpdateAction sql.NullString
		var charMaxLength, numericPrecision, numericScale sql.NullInt64
		var isPrimary, isUnique int
		var ordinalPosition int

		err := rows.Scan(
			&tableName,
			&column.Name,
			&dataType,
			&isNullable,
			&columnDefault,
			&charMaxLength,
			&numericPrecision,
			&numericScale,
			&columnType,
			&isPrimary,
			&isUnique,
			&extra,
			&ordinalPosition,
			&referencedTable,
			&referencedColumn,
			&onDeleteAction,
			&onUpdateAction,
		)
		if err != nil {
			return nil, err
		}

		column.Type = m.formatMySQLType(dataType, columnType, charMaxLength, numericPrecision, numericScale)
		column.Nullable = isNullable == "YES"
		column.IsPrimary = isPrimary == 1
		column.IsUnique = isUnique == 1
		column.IsAutoIncrement = strings.Contains(strings.ToLower(extra), "auto_increment")
		if columnDefault.Valid {
			column.Default = m.formatMySQLDefault(columnDefault.String, column.Type)
		}

		if referencedTable.Valid && referencedColumn.Valid {
			column.ForeignKeyTable = referencedTable.String
			column.ForeignKeyColumn = referencedColumn.String
			if onDeleteAction.Valid && onDeleteAction.String != "NO ACTION" {
				column.OnDeleteAction = onDeleteAction.String
			}
			if onUpdateAction.Valid && onUpdateAction.String != "NO ACTION" {
				column.OnUpdateAction = onUpdateAction.String
			}
		}

		result[tableName] = append(result[tableName], column)
	}

	// Fetch CHECK constraints for MySQL 8.0.16+
	checkQuery := fmt.Sprintf(`
		SELECT cc.TABLE_NAME, cc.CONSTRAINT_NAME, cc.CHECK_CLAUSE
		FROM information_schema.CHECK_CONSTRAINTS cc
		JOIN information_schema.TABLE_CONSTRAINTS tc
			ON cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
			AND cc.CONSTRAINT_SCHEMA = tc.TABLE_SCHEMA
		WHERE tc.TABLE_NAME IN (%s)
		  AND tc.CONSTRAINT_SCHEMA = DATABASE()
		  AND tc.CONSTRAINT_TYPE = 'CHECK'
	`, strings.Join(placeholders, ","))

	checkRows, err := m.db.QueryContext(ctx, checkQuery, args...)
	if err == nil {
		defer checkRows.Close()
		for checkRows.Next() {
			var tableName, constraintName, checkClause string
			if err := checkRows.Scan(&tableName, &constraintName, &checkClause); err != nil {
				continue
			}
			// constraintName is like "users_age_check" — try to match to column
			colName := extractColumnFromCheckName(constraintName, tableName)
			if cols, ok := result[tableName]; ok {
				for i := range cols {
					if strings.EqualFold(cols[i].Name, colName) {
						result[tableName][i].Check = checkClause
						break
					}
				}
			}
		}
	}
	// Note: if CHECK_CONSTRAINTS table doesn't exist (MySQL < 8.0.16), we silently skip

	return result, nil
}

func (m *Adapter) GetAllTablesIndexes(ctx context.Context, tableNames []string) (map[string][]types.SchemaIndex, error) {
	if len(tableNames) == 0 {
		return make(map[string][]types.SchemaIndex), nil
	}

	placeholders := make([]string, len(tableNames))
	args := make([]any, len(tableNames))
	for i, name := range tableNames {
		placeholders[i] = "?"
		args[i] = name
	}

	// These cannot be dropped independently and cause errors like PostgreSQL had
	query := fmt.Sprintf(`
		SELECT s.table_name, s.index_name, s.column_name, s.non_unique, s.seq_in_index
		FROM information_schema.statistics s
		WHERE s.table_name IN (%s) 
		  AND s.table_schema = DATABASE() 
		  AND s.index_name != 'PRIMARY'
		  AND s.index_name NOT IN (
		      -- Exclude indexes created by UNIQUE constraints
		      SELECT DISTINCT constraint_name 
		      FROM information_schema.table_constraints 
		      WHERE constraint_type = 'UNIQUE' 
		        AND table_schema = DATABASE()
		        AND table_name IN (%s)
		  )
		ORDER BY s.table_name, s.index_name, s.seq_in_index
	`, strings.Join(placeholders, ","), strings.Join(placeholders, ","))

	// Double the args since we use tableNames twice in the query
	allArgs := make([]any, len(args)*2)
	copy(allArgs, args)
	copy(allArgs[len(args):], args)

	rows, err := m.db.QueryContext(ctx, query, allArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type indexKey struct {
		tableName string
		indexName string
	}
	indexMap := make(map[indexKey]*types.SchemaIndex)

	for rows.Next() {
		var tableName, indexName, columnName string
		var nonUnique, seqInIndex int

		if err := rows.Scan(&tableName, &indexName, &columnName, &nonUnique, &seqInIndex); err != nil {
			continue
		}

		key := indexKey{tableName, indexName}
		if idx, exists := indexMap[key]; exists {
			idx.Columns = append(idx.Columns, columnName)
		} else {
			indexMap[key] = &types.SchemaIndex{
				Name:    indexName,
				Table:   tableName,
				Columns: []string{columnName},
				Unique:  nonUnique == 0,
			}
		}
	}

	result := make(map[string][]types.SchemaIndex)
	for key, idx := range indexMap {
		result[key.tableName] = append(result[key.tableName], *idx)
	}

	return result, nil
}

func (m *Adapter) GetKeyspaces(ctx context.Context) ([]string, error) {
	return nil, nil
}

func (m *Adapter) GetAllTableNames(ctx context.Context) ([]string, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT table_name FROM information_schema.tables 
		WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE'
		ORDER BY table_name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tables := make([]string, 0, 32)
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, err
		}
		tables = append(tables, tableName)
	}
	return tables, nil
}

// GetTableColumns - Compatibility stub, delegates to batch version
func (m *Adapter) GetTableColumns(ctx context.Context, tableName string) ([]types.SchemaColumn, error) {
	allColumns, err := m.GetAllTablesColumns(ctx, []string{tableName})
	if err != nil {
		return nil, err
	}
	return allColumns[tableName], nil
}

// GetTableIndexes - Compatibility stub, delegates to batch version
func (m *Adapter) GetTableIndexes(ctx context.Context, tableName string) ([]types.SchemaIndex, error) {
	allIndexes, err := m.GetAllTablesIndexes(ctx, []string{tableName})
	if err != nil {
		return nil, err
	}
	return allIndexes[tableName], nil
}

// PullCompleteSchema returns complete schema excluding internal tables
func (m *Adapter) PullCompleteSchema(ctx context.Context) ([]types.SchemaTable, error) {
	query := `
	SELECT
		c.TABLE_NAME,
		c.COLUMN_NAME,
		c.COLUMN_TYPE,
		c.IS_NULLABLE,
		c.COLUMN_DEFAULT,
		c.EXTRA,
		c.ORDINAL_POSITION,
		CASE WHEN c.COLUMN_KEY = 'PRI' THEN 'PRIMARY KEY' ELSE NULL END as is_primary,
		CASE WHEN c.COLUMN_KEY = 'UNI' THEN 'UNIQUE' ELSE NULL END as is_unique,
		k.REFERENCED_TABLE_NAME AS REFERENCES_TABLE,
		k.REFERENCED_COLUMN_NAME AS REFERENCES_COLUMN,
		r.DELETE_RULE AS ON_DELETE,
		r.UPDATE_RULE AS ON_UPDATE
	FROM INFORMATION_SCHEMA.COLUMNS c
	LEFT JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE k
		ON c.TABLE_SCHEMA = k.TABLE_SCHEMA
		AND c.TABLE_NAME = k.TABLE_NAME
		AND c.COLUMN_NAME = k.COLUMN_NAME
		AND k.REFERENCED_TABLE_NAME IS NOT NULL
	LEFT JOIN INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS r
		ON k.CONSTRAINT_NAME = r.CONSTRAINT_NAME
		AND k.TABLE_SCHEMA = r.CONSTRAINT_SCHEMA
	WHERE c.TABLE_SCHEMA = DATABASE()
		AND c.TABLE_NAME NOT LIKE '_flash_%'
	ORDER BY c.TABLE_NAME, c.ORDINAL_POSITION`

	rows, err := m.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query schema: %w", err)
	}
	defer rows.Close()

	tableMap := make(map[string]*types.SchemaTable)
	columnsSeen := make(map[string]map[string]bool)

	for rows.Next() {
		var tableName, columnName, columnType, isNullable string
		var ordinalPosition int
		var columnDefault, extra, isPrimary, isUnique, referencesTable, referencesColumn, onDelete, onUpdate sql.NullString

		err := rows.Scan(&tableName, &columnName, &columnType, &isNullable, &columnDefault,
			&extra, &ordinalPosition, &isPrimary, &isUnique, &referencesTable, &referencesColumn, &onDelete, &onUpdate)
		if err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		if _, exists := tableMap[tableName]; !exists {
			tableMap[tableName] = &types.SchemaTable{Name: tableName, Columns: []types.SchemaColumn{}}
			columnsSeen[tableName] = make(map[string]bool)
		}

		if columnsSeen[tableName][columnName] {
			continue
		}
		columnsSeen[tableName][columnName] = true

		formattedType := m.formatMySQLPullType(columnType)
		column := types.SchemaColumn{
			Name:      columnName,
			Type:      formattedType,
			Nullable:  isNullable == "YES",
			Default:   m.formatMySQLDefault(columnDefault.String, formattedType),
			IsPrimary: isPrimary.Valid,
			IsUnique:  isUnique.Valid,
		}

		if referencesTable.Valid && referencesColumn.Valid {
			column.ForeignKeyTable = referencesTable.String
			column.ForeignKeyColumn = referencesColumn.String
			if onDelete.Valid && onDelete.String != "NO ACTION" {
				column.OnDeleteAction = onDelete.String
			}
			if onUpdate.Valid && onUpdate.String != "NO ACTION" {
				column.OnUpdateAction = onUpdate.String
			}
		}

		tableMap[tableName].Columns = append(tableMap[tableName].Columns, column)
	}

	// Fetch CHECK constraints (MySQL 8.0.16+)
	checkRows, err := m.db.QueryContext(ctx, `
		SELECT tc.TABLE_NAME, cc.CONSTRAINT_NAME, cc.CHECK_CLAUSE
		FROM information_schema.CHECK_CONSTRAINTS cc
		JOIN information_schema.TABLE_CONSTRAINTS tc
			ON cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
			AND cc.CONSTRAINT_SCHEMA = tc.TABLE_SCHEMA
		WHERE tc.TABLE_SCHEMA = DATABASE()
		  AND tc.CONSTRAINT_TYPE = 'CHECK'
		  AND tc.TABLE_NAME NOT LIKE '_flash_%'
	`)
	if err == nil {
		defer checkRows.Close()
		for checkRows.Next() {
			var tableName, constraintName, checkClause string
			if err := checkRows.Scan(&tableName, &constraintName, &checkClause); err != nil {
				continue
			}
			if tbl, ok := tableMap[tableName]; ok {
				colName := extractColumnFromCheckName(constraintName, tableName)
				for i := range tbl.Columns {
					if strings.EqualFold(tbl.Columns[i].Name, colName) {
						tbl.Columns[i].Check = checkClause
						break
					}
				}
			}
		}
	}

	tables := make([]types.SchemaTable, 0, len(tableMap))
	for _, table := range tableMap {
		tables = append(tables, *table)
	}

	return tables, nil
}

func (m *Adapter) formatMySQLType(dataType, columnType string, charMaxLength, numericPrecision, numericScale sql.NullInt64) string {
	switch dataType {
	case "varchar":
		if charMaxLength.Valid {
			return fmt.Sprintf("VARCHAR(%d)", charMaxLength.Int64)
		}
		return "VARCHAR(255)"
	case "char":
		if charMaxLength.Valid {
			return fmt.Sprintf("CHAR(%d)", charMaxLength.Int64)
		}
		return "CHAR(1)"
	case "decimal":
		if numericPrecision.Valid && numericScale.Valid {
			return fmt.Sprintf("DECIMAL(%d,%d)", numericPrecision.Int64, numericScale.Int64)
		} else if numericPrecision.Valid {
			return fmt.Sprintf("DECIMAL(%d)", numericPrecision.Int64)
		}
		return "DECIMAL"
	default:
		if columnType != "" {
			return strings.ToUpper(columnType)
		}
		return m.MapColumnType(dataType)
	}
}

func (m *Adapter) formatMySQLPullType(columnType string) string {
	columnType = strings.ToUpper(columnType)

	if strings.HasPrefix(columnType, "INT(") {
		return "INT"
	}
	if strings.HasPrefix(columnType, "BIGINT(") {
		return "BIGINT"
	}
	if strings.HasPrefix(columnType, "SMALLINT(") {
		return "SMALLINT"
	}
	if strings.HasPrefix(columnType, "TINYINT(1)") {
		return "BOOLEAN"
	}
	if strings.HasPrefix(columnType, "TINYINT(") {
		return "TINYINT"
	}

	return columnType
}

func (m *Adapter) formatMySQLDefault(defaultValue, columnType string) string {
	if defaultValue == "" {
		return ""
	}

	if strings.Contains(strings.ToLower(defaultValue), "current_timestamp") {
		return "CURRENT_TIMESTAMP"
	}

	if strings.HasPrefix(strings.ToUpper(columnType), "ENUM(") {
		trimmed := strings.TrimSpace(defaultValue)
		if !strings.HasPrefix(trimmed, "'") && !strings.HasPrefix(trimmed, "\"") &&
			!strings.EqualFold(trimmed, "NULL") && !strings.EqualFold(trimmed, "CURRENT_TIMESTAMP") {
			return fmt.Sprintf("'%s'", trimmed)
		}
	}

	return defaultValue
}

// extractColumnFromCheckName tries to extract a column name from a constraint name
// using the convention: {table}_{column}_check → returns {column}
func extractColumnFromCheckName(constraintName, tableName string) string {
	prefix := tableName + "_"
	suffix := "_check"
	name := strings.ToLower(constraintName)
	if strings.HasPrefix(name, strings.ToLower(prefix)) {
		name = name[len(prefix):]
	}
	name = strings.TrimSuffix(name, suffix)
	return name
}
