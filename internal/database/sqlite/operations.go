package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

func (s *Adapter) tableExists(tableName string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(
		"SELECT COUNT(*) > 0 FROM sqlite_master WHERE type = 'table' AND name = ?",
		tableName).Scan(&exists)
	return exists, err
}

func (s *Adapter) columnExists(tableName, columnName string) (bool, error) {
	rows, err := s.db.QueryContext(context.Background(), fmt.Sprintf("PRAGMA table_info(\"%s\")", tableName))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull int
		var defaultValue sql.NullString
		var pk int

		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err == nil && name == columnName {
			return true, nil
		}
	}
	return false, nil
}

func (s *Adapter) CheckTableExists(ctx context.Context, tableName string) (bool, error) {
	return s.tableExists(tableName)
}

func (s *Adapter) CheckColumnExists(ctx context.Context, tableName, columnName string) (bool, error) {
	return s.columnExists(tableName, columnName)
}

func (s *Adapter) CheckNotNullConstraint(ctx context.Context, tableName, columnName string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(\"%s\")", tableName))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull int
		var defaultValue sql.NullString
		var pk int

		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err == nil && name == columnName {
			return notNull == 1, nil
		}
	}
	return false, nil
}

func (s *Adapter) CheckForeignKeyConstraint(ctx context.Context, tableName, constraintName string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("PRAGMA foreign_key_list(\"%s\")", tableName))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err == nil {
			if strings.Contains(constraintName, table) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *Adapter) CheckUniqueConstraint(ctx context.Context, tableName, constraintName string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("PRAGMA index_list(\"%s\")", tableName))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var seq int
		var indexName string
		var unique int
		var origin, partial string
		if err := rows.Scan(&seq, &indexName, &unique, &origin, &partial); err == nil {
			if indexName == constraintName && unique == 1 {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *Adapter) GetTableData(ctx context.Context, tableName string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("SELECT * FROM \"%s\"", tableName))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var result []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		valuePtrs := make([]any, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			continue
		}

		row := make(map[string]any)
		for i, col := range columns {
			row[col] = formatValue(values[i])
		}
		result = append(result, row)
	}
	return result, nil
}

// formatValue converts database values to display-friendly formats
func formatValue(val any) any {
	if val == nil {
		return nil
	}

	if bytes, ok := val.([]byte); ok {
		if len(bytes) == 16 {
			return formatUUID(bytes)
		}
		str := string(bytes)
		isPrintable := true
		for _, r := range str {
			if r < 32 && r != '\n' && r != '\r' && r != '\t' {
				isPrintable = false
				break
			}
		}
		if isPrintable {
			return str
		}
		return fmt.Sprintf("0x%x", bytes)
	}

	return val
}

// formatUUID converts a 16-byte slice to UUID string format
func formatUUID(bytes []byte) string {
	if len(bytes) != 16 {
		return fmt.Sprintf("%v", bytes)
	}
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		bytes[0:4],
		bytes[4:6],
		bytes[6:8],
		bytes[8:10],
		bytes[10:16])
}

func (s *Adapter) GetTableRowCount(ctx context.Context, tableName string) (int, error) {
	var count int
	query := fmt.Sprintf("SELECT COUNT(*) FROM \"%s\"", tableName)
	err := s.db.QueryRowContext(ctx, query).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count rows in table %s: %w", tableName, err)
	}
	return count, nil
}

func (s *Adapter) GetAllTableRowCounts(ctx context.Context, tableNames []string) (map[string]int, error) {
	if len(tableNames) == 0 {
		return make(map[string]int), nil
	}

	var queryParts []string
	for _, tableName := range tableNames {
		queryParts = append(queryParts, fmt.Sprintf("SELECT '%s' as table_name, COUNT(*) as row_count FROM \"%s\"", tableName, tableName))
	}

	query := strings.Join(queryParts, " UNION ALL ")
	rows, err := s.db.QueryContext(ctx, query)
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

func (s *Adapter) DropTable(ctx context.Context, tableName string) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS \"%s\"", tableName))
	return err
}

func (s *Adapter) DropEnum(ctx context.Context, enumName string) error {
	return nil
}

func (s *Adapter) GenerateCreateTableSQL(table types.SchemaTable) string {
	var lines []string
	var foreignKeys []string

	var pkCols []string
	for _, col := range table.Columns {
		if col.IsPrimary {
			pkCols = append(pkCols, fmt.Sprintf("\"%s\"", col.Name))
		}
	}
	isCompositePK := len(pkCols) > 1

	for _, column := range table.Columns {
		if column.ForeignKeyTable != "" && column.ForeignKeyColumn != "" {
			fk := fmt.Sprintf("  FOREIGN KEY (\"%s\") REFERENCES \"%s\"(\"%s\")",
				column.Name, column.ForeignKeyTable, column.ForeignKeyColumn)
			if column.OnDeleteAction != "" {
				fk += fmt.Sprintf(" ON DELETE %s", column.OnDeleteAction)
			}
			foreignKeys = append(foreignKeys, fk)
		}
	}

	lines = append(lines, fmt.Sprintf("CREATE TABLE IF NOT EXISTS \"%s\" (", table.Name))

	for i, column := range table.Columns {
		comma := ","
		isLast := i == len(table.Columns)-1
		if isLast && len(foreignKeys) == 0 && !isCompositePK {
			comma = ""
		}
		col := column
		if isCompositePK {
			col.IsPrimary = false
		}
		lines = append(lines, fmt.Sprintf("  \"%s\" %s%s", col.Name, s.FormatColumnType(col), comma))
	}

	if isCompositePK {
		sep := ","
		if len(foreignKeys) == 0 {
			sep = ""
		}
		lines = append(lines, fmt.Sprintf("  PRIMARY KEY (%s)%s", strings.Join(pkCols, ", "), sep))
	}

	for i, fk := range foreignKeys {
		comma := ","
		if i == len(foreignKeys)-1 {
			comma = ""
		}
		lines = append(lines, fmt.Sprintf("%s%s", fk, comma))
	}

	lines = append(lines, ");")
	return strings.Join(lines, "\n")
}

func (s *Adapter) GenerateAddColumnSQL(tableName string, column types.SchemaColumn) string {
	return fmt.Sprintf("ALTER TABLE \"%s\" ADD COLUMN \"%s\" %s;",
		tableName, column.Name, s.FormatColumnType(column))
}

// GenerateDropColumnSQL generates SQL to drop a column in SQLite
// NOTE: DROP COLUMN requires SQLite version 3.35.0+ (released March 2021).
// Older versions will fail with a syntax error. If you need to support older
// SQLite versions, you must recreate the table without the dropped column.
func (s *Adapter) GenerateDropColumnSQL(tableName, columnName string) string {
	return fmt.Sprintf("ALTER TABLE \"%s\" DROP COLUMN \"%s\";", tableName, columnName)
}

func (s *Adapter) GenerateAlterColumnSQL(tableName string, column types.SchemaColumn, oldType string) string {
	// SQLite does not support ALTER COLUMN TYPE natively.
	// Column modifications require table recreation, which is too risky to automate here.
	return ""
}

func (s *Adapter) GenerateAddIndexSQL(index types.SchemaIndex) string {
	unique := ""
	if index.Unique {
		unique = "UNIQUE "
	}
	columns := "\"" + strings.Join(index.Columns, "\", \"") + "\""
	sql := fmt.Sprintf("CREATE %sINDEX \"%s\" ON \"%s\" (%s)", unique, index.Name, index.Table, columns)
	if index.Where != "" {
		sql += fmt.Sprintf(" WHERE %s", index.Where)
	}
	return sql + ";"
}

func (s *Adapter) GenerateDropIndexSQL(index types.SchemaIndex) string {
	return fmt.Sprintf("DROP INDEX IF EXISTS \"%s\";", index.Name)
}

func (s *Adapter) FormatColumnType(column types.SchemaColumn) string {
	parts := []string{column.Type}

	if column.IsPrimary {
		if strings.ToUpper(column.Type) == "INTEGER" {
			parts = append(parts, "PRIMARY KEY AUTOINCREMENT")
		} else {
			parts = append(parts, "PRIMARY KEY")
		}
	}
	if column.IsUnique && !column.IsPrimary {
		parts = append(parts, "UNIQUE")
	}
	if !column.Nullable {
		parts = append(parts, "NOT NULL")
	}
	if column.Default != "" {
		parts = append(parts, fmt.Sprintf("DEFAULT %s", column.Default))
	}
	if column.Generated != "" {
		parts = append(parts, fmt.Sprintf("GENERATED ALWAYS AS (%s) STORED", column.Generated))
	}
	if column.Check != "" {
		parts = append(parts, fmt.Sprintf("CHECK (%s)", column.Check))
	}

	return strings.Join(parts, " ")
}
