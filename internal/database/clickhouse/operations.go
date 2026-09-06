package clickhouse

import (
	"context"
	"fmt"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

func (a *Adapter) GenerateCreateTableSQL(table types.SchemaTable) string {
	var lines []string
	var pkCols []string

	lines = append(lines, fmt.Sprintf("CREATE TABLE IF NOT EXISTS `%s` (", table.Name))

	for i, col := range table.Columns {
		comma := ","
		if i == len(table.Columns)-1 {
			comma = ""
		}
		lines = append(lines, fmt.Sprintf("    `%s` %s%s", col.Name, a.FormatColumnType(col), comma))
		if col.IsPrimary {
			pkCols = append(pkCols, "`"+col.Name+"`")
		}
	}
	lines = append(lines, ")")
	lines = append(lines, "ENGINE = MergeTree()")

	if len(pkCols) > 0 {
		lines = append(lines, fmt.Sprintf("ORDER BY (%s);", strings.Join(pkCols, ", ")))
	} else if len(table.Columns) > 0 {
		lines = append(lines, fmt.Sprintf("ORDER BY (`%s`);", table.Columns[0].Name))
	} else {
		lines = append(lines, "ORDER BY tuple();")
	}

	return strings.Join(lines, "\n")
}

func (a *Adapter) GenerateAddColumnSQL(tableName string, column types.SchemaColumn) string {
	return fmt.Sprintf("ALTER TABLE `%s` ADD COLUMN IF NOT EXISTS `%s` %s;",
		tableName, column.Name, a.FormatColumnType(column))
}

func (a *Adapter) GenerateDropColumnSQL(tableName, columnName string) string {
	return fmt.Sprintf("ALTER TABLE `%s` DROP COLUMN IF EXISTS `%s`;", tableName, columnName)
}

func (a *Adapter) GenerateAlterColumnSQL(tableName string, column types.SchemaColumn, oldType string) string {
	if column.Type == oldType {
		return ""
	}
	return fmt.Sprintf("ALTER TABLE `%s` MODIFY COLUMN `%s` %s;",
		tableName, column.Name, a.FormatColumnType(column))
}

func (a *Adapter) GenerateAddIndexSQL(index types.SchemaIndex) string {
	cols := strings.Join(index.Columns, ", ")
	indexType := "minmax"
	if index.Unique {
		indexType = "bloom_filter"
	}
	return fmt.Sprintf("ALTER TABLE `%s` ADD INDEX `%s` (%s) TYPE %s GRANULARITY 1;",
		index.Table, index.Name, cols, indexType)
}

func (a *Adapter) GenerateDropIndexSQL(index types.SchemaIndex) string {
	return fmt.Sprintf("ALTER TABLE `%s` DROP INDEX IF EXISTS `%s`;", index.Table, index.Name)
}

func (a *Adapter) FormatColumnType(column types.SchemaColumn) string {
	t := column.Type
	if column.Nullable && !strings.HasPrefix(strings.ToLower(t), "nullable(") {
		t = fmt.Sprintf("Nullable(%s)", t)
	}
	if column.Default != "" {
		t += fmt.Sprintf(" DEFAULT %s", column.Default)
	}
	return t
}

func (a *Adapter) CheckTableExists(ctx context.Context, tableName string) (bool, error) {
	db := a.currentDatabase(ctx)
	var count uint64
	err := a.db.QueryRowContext(ctx,
		`SELECT count() FROM system.tables WHERE database = ? AND name = ?`, db, tableName).Scan(&count)
	return count > 0, err
}

func (a *Adapter) CheckColumnExists(ctx context.Context, tableName, columnName string) (bool, error) {
	db := a.currentDatabase(ctx)
	var count uint64
	err := a.db.QueryRowContext(ctx,
		`SELECT count() FROM system.columns WHERE database = ? AND table = ? AND name = ?`,
		db, tableName, columnName).Scan(&count)
	return count > 0, err
}

func (a *Adapter) CheckNotNullConstraint(ctx context.Context, tableName, columnName string) (bool, error) {
	db := a.currentDatabase(ctx)
	var chType string
	err := a.db.QueryRowContext(ctx,
		`SELECT type FROM system.columns WHERE database = ? AND table = ? AND name = ?`,
		db, tableName, columnName).Scan(&chType)
	if err != nil {
		return false, err
	}
	return !strings.HasPrefix(strings.ToLower(chType), "nullable("), nil
}

func (a *Adapter) CheckForeignKeyConstraint(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

func (a *Adapter) CheckUniqueConstraint(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

func (a *Adapter) GetTableData(ctx context.Context, tableName string) ([]map[string]any, error) {
	rows, err := a.db.QueryContext(ctx, fmt.Sprintf("SELECT * FROM `%s`", tableName))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	var result []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			row[c] = vals[i]
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (a *Adapter) GetTableRowCount(ctx context.Context, tableName string) (int, error) {
	var count uint64
	err := a.db.QueryRowContext(ctx, fmt.Sprintf("SELECT count() FROM `%s`", tableName)).Scan(&count)
	return int(count), err
}

func (a *Adapter) GetAllTableRowCounts(ctx context.Context, tableNames []string) (map[string]int, error) {
	result := make(map[string]int, len(tableNames))
	for _, t := range tableNames {
		count, err := a.GetTableRowCount(ctx, t)
		if err != nil {
			result[t] = 0
			continue
		}
		result[t] = count
	}
	return result, nil
}

func (a *Adapter) DropTable(ctx context.Context, tableName string) error {
	_, err := a.db.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS `%s`", tableName))
	return err
}

func (a *Adapter) DropEnum(_ context.Context, _ string) error {
	return nil
}
