package scylla

import (
	"context"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

// resolveTable splits a potentially keyspace-qualified name into (keyspace, tablename).
// If no keyspace prefix, fall back to adapter's current keyspace.
func (a *Adapter) resolveTable(tableName string) (string, string) {
	if before, after, ok := strings.Cut(tableName, "."); ok {
		return before, after
	}
	return a.currentKeyspace(), tableName
}

func (a *Adapter) GetAllTableNames(ctx context.Context) ([]string, error) {
	keyspace := a.keyspace
	query := "SELECT table_name FROM system_schema.tables WHERE keyspace_name = ?"
	iter := a.session.Query(query, keyspace).IterContext(ctx)
	defer iter.Close()

	var names []string
	for {
		var name string
		if !iter.Scan(&name) {
			break
		}
		names = append(names, keyspace+"."+name)
	}
	if err := iter.Close(); err != nil {
		return names, err
	}
	return names, nil
}

func (a *Adapter) GetKeyspaces(ctx context.Context) ([]string, error) {
	query := "SELECT keyspace_name FROM system_schema.keyspaces"
	iter := a.session.Query(query).IterContext(ctx)
	defer iter.Close()

	var keyspaces []string
	for {
		var ks string
		if !iter.Scan(&ks) {
			break
		}
		keyspaces = append(keyspaces, ks)
	}
	if err := iter.Close(); err != nil {
		return keyspaces, err
	}
	return keyspaces, nil
}

func (a *Adapter) GetCurrentSchema(ctx context.Context) ([]types.SchemaTable, error) {
	return a.PullCompleteSchema(ctx)
}

func (a *Adapter) GetCurrentEnums(_ context.Context) ([]types.SchemaEnum, error) {
	return nil, nil
}

func (a *Adapter) GetTableColumns(ctx context.Context, tableName string) ([]types.SchemaColumn, error) {
	ks, tbl := a.resolveTable(tableName)
	cacheKey := ks + "." + tbl

	if cols, ok := a.cache.get(cacheKey); ok {
		return cols, nil
	}

	iter := a.session.Query(
		`SELECT column_name, type, kind FROM system_schema.columns WHERE keyspace_name = ? AND table_name = ? ALLOW FILTERING`,
		ks, tbl,
	).IterContext(ctx)
	defer iter.Close()

	var cols []types.SchemaColumn
	var name, cqlType, kind string
	for iter.Scan(&name, &cqlType, &kind) {
		cols = append(cols, types.SchemaColumn{
			Name:      name,
			Type:      cqlType,
			Nullable:  kind != "partition_key",
			IsPrimary: kind == "partition_key" || kind == "clustering",
		})
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	a.cache.set(cacheKey, cols)
	return cols, nil
}

func (a *Adapter) GetTableIndexes(_ context.Context, _ string) ([]types.SchemaIndex, error) {
	return nil, nil
}

func (a *Adapter) PullCompleteSchema(ctx context.Context) ([]types.SchemaTable, error) {
	ks := a.currentKeyspace()
	if ks == "" || ks == "system" {
		return nil, nil
	}
	iter := a.session.Query(
		`SELECT table_name, column_name, type, kind FROM system_schema.columns WHERE keyspace_name = ? ALLOW FILTERING`, ks,
	).IterContext(ctx)
	defer iter.Close()

	tableMap := make(map[string]*types.SchemaTable)
	var order []string

	var tableName, colName, cqlType, kind string
	for iter.Scan(&tableName, &colName, &cqlType, &kind) {
		if strings.HasPrefix(tableName, "_flash_") {
			continue
		}
		if _, ok := tableMap[tableName]; !ok {
			tableMap[tableName] = &types.SchemaTable{Name: tableName}
			order = append(order, tableName)
		}
		tableMap[tableName].Columns = append(tableMap[tableName].Columns, types.SchemaColumn{
			Name:      colName,
			Type:      cqlType,
			Nullable:  kind != "partition_key",
			IsPrimary: kind == "partition_key" || kind == "clustering",
		})
	}
	if err := iter.Close(); err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return nil, nil
		}
		return nil, err
	}

	tables := make([]types.SchemaTable, 0, len(order))
	for _, name := range order {
		tables = append(tables, *tableMap[name])
	}
	return tables, nil
}
