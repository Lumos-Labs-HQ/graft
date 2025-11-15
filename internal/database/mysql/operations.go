package mysql

import (
	"context"
	"fmt"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

func (m *Adapter) tableExists(tableName string) (bool, error) {
	var exists bool
	err := m.db.QueryRow(`
		SELECT COUNT(*) > 0 FROM information_schema.tables 
		WHERE table_name = ? AND table_schema = DATABASE()
	`, tableName).Scan(&exists)
	return exists, err
}

func (m *Adapter) columnExists(tableName, columnName string) (bool, error) {
	var exists bool
	err := m.db.QueryRow(`
		SELECT COUNT(*) > 0 FROM information_schema.columns 
		WHERE table_name = ? AND column_name = ? AND table_schema = DATABASE()
	`, tableName, columnName).Scan(&exists)
	return exists, err
}

func (m *Adapter) CheckTableExists(ctx context.Context, tableName string) (bool, error) {
	return m.tableExists(tableName)
}

func (m *Adapter) CheckColumnExists(ctx context.Context, tableName, columnName string) (bool, error) {
	return m.columnExists(tableName, columnName)
}

func (m *Adapter) CheckNotNullConstraint(ctx context.Context, tableName, columnName string) (bool, error) {
	var isNullable string
	err := m.db.QueryRowContext(ctx, `
		SELECT is_nullable FROM information_schema.columns 
		WHERE table_name = ? AND column_name = ? AND table_schema = DATABASE()
	`, tableName, columnName).Scan(&isNullable)
	if err != nil {
		return false, err
	}
	return isNullable == "NO", nil
}

func (m *Adapter) CheckForeignKeyConstraint(ctx context.Context, tableName, constraintName string) (bool, error) {
	return m.checkConstraint(tableName, constraintName, "FOREIGN KEY")
}

func (m *Adapter) CheckUniqueConstraint(ctx context.Context, tableName, constraintName string) (bool, error) {
	return m.checkConstraint(tableName, constraintName, "UNIQUE")
}

func (m *Adapter) checkConstraint(tableName, constraintName, constraintType string) (bool, error) {
	var exists bool
	err := m.db.QueryRow(`
		SELECT COUNT(*) > 0 FROM information_schema.table_constraints 
		WHERE table_name = ? AND constraint_name = ? AND constraint_type = ? AND table_schema = DATABASE()
	`, tableName, constraintName, constraintType).Scan(&exists)
	return exists, err
}

func (m *Adapter) GetTableData(ctx context.Context, tableName string) ([]map[string]interface{}, error) {
	rows, err := m.db.QueryContext(ctx, fmt.Sprintf("SELECT * FROM `%s`", tableName))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var result []map[string]interface{}
	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, err
		}

		row := make(map[string]interface{})
		for i, col := range columns {
			row[col] = values[i]
		}
		result = append(result, row)
	}
	return result, nil
}

func (m *Adapter) GetTableRowCount(ctx context.Context, tableName string) (int, error) {
	var count int
	query := fmt.Sprintf("SELECT COUNT(*) FROM `%s`", tableName)
	err := m.db.QueryRowContext(ctx, query).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count rows in table %s: %w", tableName, err)
	}
	return count, nil
}

func (m *Adapter) GetAllTableRowCounts(ctx context.Context, tableNames []string) (map[string]int, error) {
	if len(tableNames) == 0 {
		return make(map[string]int), nil
	}

	var queryParts []string
	for _, tableName := range tableNames {
		queryParts = append(queryParts, fmt.Sprintf("SELECT '%s' as table_name, COUNT(*) as row_count FROM `%s`", tableName, tableName))
	}

	query := strings.Join(queryParts, " UNION ALL ")
	rows, err := m.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to batch count table rows: %w", err)
	}
	defer rows.Close()

	result := make(map[string]int, len(tableNames))
	for rows.Next() {
		var tableName string
		var count int
		if err := rows.Scan(&tableName, &count); err != nil {
			return nil, fmt.Errorf("failed to scan batch count result: %w", err)
		}
		result[tableName] = count
	}

	return result, nil
}

func (m *Adapter) DropTable(ctx context.Context, tableName string) error {
	_, err := m.db.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS `%s`", tableName))
	return err
}

func (m *Adapter) DropEnum(ctx context.Context, enumName string) error {
	return nil
}

func (m *Adapter) GenerateCreateTableSQL(table types.SchemaTable) string {
	var lines []string
	var foreignKeys []string

	for _, column := range table.Columns {
		if column.ForeignKeyTable != "" && column.ForeignKeyColumn != "" {
			fk := fmt.Sprintf("  FOREIGN KEY (`%s`) REFERENCES `%s`(`%s`)",
				column.Name, column.ForeignKeyTable, column.ForeignKeyColumn)
			if column.OnDeleteAction != "" {
				fk += fmt.Sprintf(" ON DELETE %s", column.OnDeleteAction)
			}
			foreignKeys = append(foreignKeys, fk)
		}
	}

	lines = append(lines, fmt.Sprintf("CREATE TABLE IF NOT EXISTS `%s` (", table.Name))

	for i, column := range table.Columns {
		comma := ","
		if i == len(table.Columns)-1 && len(foreignKeys) == 0 {
			comma = ""
		}
		lines = append(lines, fmt.Sprintf("  `%s` %s%s", column.Name, m.FormatColumnType(column), comma))
	}

	for i, fk := range foreignKeys {
		comma := ","
		if i == len(foreignKeys)-1 {
			comma = ""
		}
		lines = append(lines, fmt.Sprintf("%s%s", fk, comma))
	}

	lines = append(lines, ") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;")
	return strings.Join(lines, "\n")
}

func (m *Adapter) GenerateAddColumnSQL(tableName string, column types.SchemaColumn) string {
	return fmt.Sprintf("ALTER TABLE `%s` ADD COLUMN `%s` %s;",
		tableName, column.Name, m.FormatColumnType(column))
}

func (m *Adapter) GenerateDropColumnSQL(tableName, columnName string) string {
	return fmt.Sprintf("ALTER TABLE `%s` DROP COLUMN `%s`;", tableName, columnName)
}

func (m *Adapter) GenerateAddIndexSQL(index types.SchemaIndex) string {
	unique := ""
	if index.Unique {
		unique = "UNIQUE "
	}
	columns := "`" + strings.Join(index.Columns, "`, `") + "`"
	return fmt.Sprintf("CREATE %sINDEX `%s` ON `%s` (%s);", unique, index.Name, index.Table, columns)
}

func (m *Adapter) GenerateDropIndexSQL(indexName string) string {
	return fmt.Sprintf("DROP INDEX `%s`;", indexName)
}

func (m *Adapter) FormatColumnType(column types.SchemaColumn) string {
	var parts []string
	columnType := m.convertTypeToMySQL(column.Type)
	parts = append(parts, columnType)

	if column.IsPrimary {
		parts = append(parts, "PRIMARY KEY")
		if strings.Contains(strings.ToUpper(columnType), "INT") {
			parts = append(parts, "AUTO_INCREMENT")
		}
	}

	if column.IsUnique && !column.IsPrimary {
		parts = append(parts, "UNIQUE")
	}

	if !column.Nullable && !column.IsPrimary {
		parts = append(parts, "NOT NULL")
	}

	if column.Default != "" {
		defaultValue := column.Default
		if strings.HasPrefix(strings.ToUpper(columnType), "ENUM(") {
			trimmed := strings.TrimSpace(defaultValue)
			if !strings.HasPrefix(trimmed, "'") && !strings.HasPrefix(trimmed, "\"") &&
				!strings.EqualFold(trimmed, "NULL") && !strings.EqualFold(trimmed, "CURRENT_TIMESTAMP") {
				defaultValue = fmt.Sprintf("'%s'", trimmed)
			}
		}
		parts = append(parts, fmt.Sprintf("DEFAULT %s", defaultValue))
	}

	return strings.Join(parts, " ")
}


func (m *Adapter) convertTypeToMySQL(pgType string) string {
	upperType := strings.ToUpper(pgType)
	
	if upperType == "SERIAL" {
		return "INT"
	}
	if upperType == "BIGSERIAL" {
		return "BIGINT"
	}
	if upperType == "SMALLSERIAL" {
		return "SMALLINT"
	}
	
	if strings.Contains(upperType, "TIMESTAMP WITH TIME ZONE") || strings.Contains(upperType, "TIMESTAMPTZ") {
		return "TIMESTAMP"
	}
	
	if upperType == "BOOLEAN" || upperType == "BOOL" {
		return "TINYINT(1)"
	}
	
	return pgType
}
