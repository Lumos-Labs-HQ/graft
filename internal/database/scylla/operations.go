package scylla

import (
	"context"
	"fmt"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

// qualifiedTableName returns a fully-qualified table reference for CQL.
// If tableName already contains a dot (e.g. "visa_app.applications"), it is
// treated as keyspace-qualified and returned as-is (after quoting each part).
// Otherwise the current keyspace is prepended.
func (a *Adapter) qualifiedTableName(tableName string) string {
	if before, after, ok := strings.Cut(tableName, "."); ok {
		ks := stripNameQuotes(strings.TrimSpace(before))
		tbl := stripNameQuotes(strings.TrimSpace(after))
		return fmt.Sprintf(`"%s"."%s"`, ks, tbl)
	}
	ks := a.currentKeyspace()
	tbl := stripNameQuotes(tableName)
	return fmt.Sprintf(`"%s"."%s"`, ks, tbl)
}

func stripNameQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func (a *Adapter) GenerateCreateTableSQL(table types.SchemaTable) string {
	tblRef := a.qualifiedTableName(table.Name)

	if len(table.Columns) == 0 {
		return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (unused text PRIMARY KEY);`, tblRef)
	}

	var lines []string
	lines = append(lines, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (`, tblRef))
	for _, col := range table.Columns {
		lines = append(lines, fmt.Sprintf(`    "%s" %s,`, col.Name, a.FormatColumnType(col)))
	}

	if table.CompositePK != "" {
		lines = append(lines, fmt.Sprintf(`    PRIMARY KEY %s`, table.CompositePK))
	} else {
		var pk []string
		for _, col := range table.Columns {
			if col.IsPrimary {
				pk = append(pk, `"`+col.Name+`"`)
			}
		}
		if len(pk) == 0 {
			pk = []string{`"` + table.Columns[0].Name + `"`}
		}
		lines = append(lines, fmt.Sprintf(`    PRIMARY KEY (%s)`, strings.Join(pk, ", ")))
	}

	lines = append(lines, ");")
	return strings.Join(lines, "\n")
}

func (a *Adapter) GenerateAddColumnSQL(tableName string, column types.SchemaColumn) string {
	return fmt.Sprintf(`ALTER TABLE %s ADD "%s" %s;`,
		a.qualifiedTableName(tableName), column.Name, a.FormatColumnType(column))
}

func (a *Adapter) GenerateDropColumnSQL(tableName, columnName string) string {
	return fmt.Sprintf(`ALTER TABLE %s DROP "%s";`,
		a.qualifiedTableName(tableName), columnName)
}

func (a *Adapter) GenerateAlterColumnSQL(tableName string, column types.SchemaColumn, oldType string) string {
	if column.Type == oldType {
		return ""
	}
	return fmt.Sprintf(`ALTER TABLE %s ALTER "%s" TYPE %s;`,
		a.qualifiedTableName(tableName), column.Name, a.FormatColumnType(column))
}

func (a *Adapter) GenerateAddIndexSQL(index types.SchemaIndex) string {
	cols := strings.Join(index.Columns, `", "`)
	return fmt.Sprintf(`CREATE INDEX IF NOT EXISTS "%s" ON %s ("%s");`,
		index.Name, a.qualifiedTableName(index.Table), cols)
}

func (a *Adapter) GenerateDropIndexSQL(index types.SchemaIndex) string {
	return fmt.Sprintf(`DROP INDEX IF EXISTS %s;`, a.qualifiedTableName(index.Table))
}

func (a *Adapter) FormatColumnType(column types.SchemaColumn) string {
	return column.Type
}

func (a *Adapter) CheckTableExists(ctx context.Context, tableName string) (bool, error) {
	tblRef := a.qualifiedTableName(tableName)
	ks, tbl := splitQualified(tblRef)
	var count int
	err := a.session.Query(
		`SELECT count(*) FROM system_schema.tables WHERE keyspace_name = ? AND table_name = ? ALLOW FILTERING`,
		ks, tbl,
	).ScanContext(ctx, &count)
	return count > 0, err
}

func (a *Adapter) CheckColumnExists(ctx context.Context, tableName, columnName string) (bool, error) {
	tblRef := a.qualifiedTableName(tableName)
	ks, tbl := splitQualified(tblRef)
	var count int
	err := a.session.Query(
		`SELECT count(*) FROM system_schema.columns WHERE keyspace_name = ? AND table_name = ? AND column_name = ? ALLOW FILTERING`,
		ks, tbl, columnName,
	).ScanContext(ctx, &count)
	return count > 0, err
}

func (a *Adapter) CheckNotNullConstraint(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

func (a *Adapter) CheckForeignKeyConstraint(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

func (a *Adapter) CheckUniqueConstraint(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

func (a *Adapter) GetTableData(ctx context.Context, tableName string) ([]map[string]any, error) {
	tblRef := a.qualifiedTableName(tableName)
	q := a.session.Query(fmt.Sprintf(`SELECT * FROM %s`, tblRef))
	q.PageSize(500)
	iter := q.IterContext(ctx)
	defer iter.Close()

	var result []map[string]any
	for {
		m := make(map[string]any)
		if !iter.MapScan(m) {
			break
		}
		result = append(result, m)
	}
	return result, iter.Close()
}

func (a *Adapter) GetTableRowCount(ctx context.Context, tableName string) (int, error) {
	tblRef := a.qualifiedTableName(tableName)
	var count int
	err := a.session.Query(fmt.Sprintf(`SELECT count(*) FROM %s`, tblRef)).ScanContext(ctx, &count)
	return count, err
}

func (a *Adapter) GetAllTableRowCounts(ctx context.Context, tableNames []string) (map[string]int, error) {
	type entry struct {
		name  string
		count int
	}
	ch := make(chan entry, len(tableNames))
	for _, t := range tableNames {
		go func(tbl string) {
			count, err := a.GetTableRowCount(ctx, tbl)
			if err != nil {
				count = 0
			}
			ch <- entry{tbl, count}
		}(t)
	}
	result := make(map[string]int, len(tableNames))
	for range tableNames {
		e := <-ch
		result[e.name] = e.count
	}
	return result, nil
}

func (a *Adapter) DropTable(ctx context.Context, tableName string) error {
	tblRef := a.qualifiedTableName(tableName)
	return a.session.Query(fmt.Sprintf(`DROP TABLE IF EXISTS %s`, tblRef)).ExecContext(ctx)
}

func (a *Adapter) DropEnum(_ context.Context, _ string) error { return nil }

// splitQualified splits a quoted fully-qualified name like "\"ks\".\"tbl\"" into ("ks", "tbl").
func splitQualified(quoted string) (string, string) {
	parts := strings.SplitN(quoted, ".", 2)
	if len(parts) == 2 {
		return strings.Trim(parts[0], `"`), strings.Trim(parts[1], `"`)
	}
	return "", strings.Trim(parts[0], `"`)
}
