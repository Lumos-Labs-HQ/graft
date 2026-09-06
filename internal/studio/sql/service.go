package sql

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Lumos-Labs-HQ/flash/internal/branch"
	"github.com/Lumos-Labs-HQ/flash/internal/config"
	"github.com/Lumos-Labs-HQ/flash/internal/database"
	"github.com/Lumos-Labs-HQ/flash/internal/studio/common"
	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

type Service struct {
	adapter database.DatabaseAdapter
	cfg     *config.Config
	ctx     context.Context
}

func NewService(adapter database.DatabaseAdapter, cfg *config.Config) *Service {
	return &Service{adapter: adapter, cfg: cfg, ctx: context.Background()}
}

func (s *Service) quote(name string) string {
	// Handle ks.table qualified names — quote each segment separately
	if before, after, ok := strings.Cut(name, "."); ok {
		return s.adapter.QuoteIdentifier(before) + "." + s.adapter.QuoteIdentifier(after)
	}
	return s.adapter.QuoteIdentifier(name)
}

func (s *Service) placeholder(n int) string {
	switch s.adapter.ProviderName() {
	case "postgresql":
		return fmt.Sprintf("$%d", n)
	default:
		return "?"
	}
}

func (s *Service) ensureCorrectSchema() error {
	if s.cfg == nil {
		return nil
	}

	// Skip branch management if using direct DB URL (--db flag)
	if s.cfg.Database.URLEnv == "STUDIO_DB_URL" {
		return nil
	}

	// Skip if migrations path is not set or is default empty
	if s.cfg.MigrationsPath == "" || s.cfg.MigrationsPath == "db/migrations" {
		return nil
	}

	branchMgr := branch.NewMetadataManager(s.cfg.MigrationsPath)
	store, err := branchMgr.Load()
	if err != nil {
		return nil
	}

	currentBranch := store.GetBranch(store.Current)
	if currentBranch == nil {
		return nil
	}

	switch s.cfg.Database.Provider {
	case "postgresql", "postgres":
		query := fmt.Sprintf("SET search_path TO %s, public", s.quote(currentBranch.Schema))
		_, err = s.adapter.ExecuteQuery(s.ctx, query)
		return err
	case "clickhouse":
		query := fmt.Sprintf("USE %s", s.quote(currentBranch.Schema))
		_, err = s.adapter.ExecuteQuery(s.ctx, query)
		return err
	case "scylla", "scylladb", "cassandra":
		if currentBranch.Schema != "" {
			type SchemaSwitcher interface {
				SetActiveSchema(ctx context.Context, schema string) error
			}
			if switcher, ok := s.adapter.(SchemaSwitcher); ok {
				return switcher.SetActiveSchema(s.ctx, currentBranch.Schema)
			}
		}
		return nil
	case "mysql", "sqlite", "sqlite3":
		type DatabaseSwitcher interface {
			SwitchDatabase(ctx context.Context, dbName string) error
		}
		if switcher, ok := s.adapter.(DatabaseSwitcher); ok {
			return switcher.SwitchDatabase(s.ctx, currentBranch.Schema)
		}
	}
	return nil
}

func (s *Service) GetTables() ([]common.TableInfo, error) {
	_ = s.ensureCorrectSchema()

	// ScyllaDB/Cassandra — return keyspace-grouped tables from all user keyspaces
	if s.adapter.ProviderName() == "scylla" || s.adapter.ProviderName() == "scylladb" || s.adapter.ProviderName() == "cassandra" {
		ks, err := s.adapter.GetKeyspaces(s.ctx)
		if err != nil {
			return nil, err
		}
		type SchemaSwitcher interface {
			SetActiveSchema(ctx context.Context, schema string) error
		}

		// Collect all user tables first, then fetch row counts concurrently.
		type entry struct {
			displayName string
			fullName    string
			keyspace    string
		}
		var entries []entry
		for _, k := range ks {
			if k == "system" || k == "system_schema" || k == "system_auth" || k == "system_distributed" || k == "system_traces" {
				continue
			}
			if sw, ok := s.adapter.(SchemaSwitcher); ok {
				_ = sw.SetActiveSchema(s.ctx, k)
			}
			tables, _ := s.adapter.GetAllTableNames(s.ctx)
			for _, t := range tables {
				if t == "_flash_migrations" {
					continue
				}
				displayName := t
				if _, after, ok := strings.Cut(t, "."); ok {
					displayName = after
				}
				entries = append(entries, entry{displayName, t, k})
			}
		}

		// Fetch all row counts concurrently.
		fullNames := make([]string, len(entries))
		for i, e := range entries {
			fullNames[i] = e.fullName
		}
		counts, _ := s.adapter.GetAllTableRowCounts(s.ctx, fullNames)

		result := make([]common.TableInfo, len(entries))
		for i, e := range entries {
			result[i] = common.TableInfo{
				Name: e.displayName, FullName: e.fullName, RowCount: counts[e.fullName], Keyspace: e.keyspace,
			}
		}
		return result, nil
	}

	tables, err := s.adapter.GetAllTableNames(s.ctx)
	if err != nil {
		return nil, err
	}

	result := make([]common.TableInfo, 0, len(tables))
	targetTables := make([]string, 0, len(tables))

	for _, table := range tables {
		if table != "_flash_migrations" {
			targetTables = append(targetTables, table)
		}
	}

	tableCounts, err := s.adapter.GetAllTableRowCounts(s.ctx, targetTables)
	if err != nil {
		tableCounts = make(map[string]int)
		for _, table := range targetTables {
			count, _ := s.adapter.GetTableRowCount(s.ctx, table)
			tableCounts[table] = count
		}
	}

	for _, table := range targetTables {
		result = append(result, common.TableInfo{Name: table, RowCount: tableCounts[table]})
	}

	return result, nil
}

func (s *Service) GetTableData(tableName string, page, limit int) (*common.TableData, error) {
	return s.GetTableDataFiltered(tableName, page, limit, nil)
}

func (s *Service) GetTableDataFiltered(tableName string, page, limit int, filters []common.Filter) (*common.TableData, error) {
	_ = s.ensureCorrectSchema()
	schema, err := s.adapter.GetTableColumns(s.ctx, tableName)
	if err != nil {
		return nil, err
	}

	// Deduplicate columns (some adapters may return duplicates)
	seen := make(map[string]bool)
	columns := make([]common.ColumnInfo, 0, len(schema))
	columnTypes := make(map[string]string)
	for _, col := range schema {
		if seen[col.Name] {
			continue // Skip duplicate column
		}
		seen[col.Name] = true
		columns = append(columns, common.ColumnInfo{
			Name:             col.Name,
			Type:             col.Type,
			Nullable:         col.Nullable,
			PrimaryKey:       col.IsPrimary,
			Default:          col.Default,
			AutoIncrement:    col.IsAutoIncrement,
			ForeignKeyTable:  col.ForeignKeyTable,
			ForeignKeyColumn: col.ForeignKeyColumn,
		})
		columnTypes[col.Name] = col.Type
	}

	offset := (page - 1) * limit

	// Build WHERE clause from filters
	whereClause, whereArgs := s.buildWhereClause(filters, columnTypes)

	rows, err := s.getRowsFiltered(tableName, limit, offset, whereClause, whereArgs)
	if err != nil {
		return nil, err
	}

	total, _ := s.getFilteredRowCount(tableName, whereClause, whereArgs)

	return &common.TableData{
		Columns: columns,
		Rows:    rows,
		Total:   total,
		Page:    page,
		Limit:   limit,
	}, nil
}

func (s *Service) SaveChanges(tableName string, changes []common.RowChange) error {
	_ = s.ensureCorrectSchema()

	if err := common.ValidateQualifiedIdentifier(tableName); err != nil {
		return fmt.Errorf("invalid table name: %w", err)
	}

	schema, err := s.adapter.GetTableColumns(s.ctx, tableName)
	if err != nil {
		return err
	}

	pkColumn := "id"
	colNullable := make(map[string]bool)
	for _, col := range schema {
		if col.IsPrimary {
			pkColumn = col.Name
		}
		colNullable[col.Name] = col.Nullable
	}

	for _, change := range changes {
		if change.Action == "update" {
			if err := common.ValidateIdentifier(change.Column); err != nil {
				return fmt.Errorf("invalid column name: %w", err)
			}

			// Reject null on NOT NULL columns before hitting the DB
			if change.Value == nil {
				if nullable, known := colNullable[change.Column]; known && !nullable {
					return fmt.Errorf("column \"%s\" does not allow NULL values", change.Column)
				}
			}

			query := fmt.Sprintf("UPDATE %s SET %s = %s WHERE %s = %s",
				s.quote(tableName), s.quote(change.Column),
				s.placeholder(1), s.quote(pkColumn), s.placeholder(2))

			if err := s.adapter.ExecuteDMLWithArgs(s.ctx, query, change.Value, change.RowID); err != nil {
				return fmt.Errorf("failed to update %s.%s: %w", tableName, change.Column, err)
			}
		}
	}
	return nil
}

func (s *Service) DeleteRows(tableName string, rowIDs []string) error {
	_ = s.ensureCorrectSchema()

	if err := common.ValidateQualifiedIdentifier(tableName); err != nil {
		return fmt.Errorf("invalid table name: %w", err)
	}

	schema, err := s.adapter.GetTableColumns(s.ctx, tableName)
	if err != nil {
		return err
	}

	pkColumn := "id"
	for _, col := range schema {
		if col.IsPrimary {
			pkColumn = col.Name
			break
		}
	}

	for _, rowID := range rowIDs {
		query := fmt.Sprintf("DELETE FROM %s WHERE %s = %s",
			s.quote(tableName), s.quote(pkColumn), s.placeholder(1))
		if err := s.adapter.ExecuteDMLWithArgs(s.ctx, query, rowID); err != nil {
			return fmt.Errorf("failed to delete row %s: %w", rowID, err)
		}
	}
	return nil
}

func (s *Service) AddRow(tableName string, data map[string]any) error {
	_ = s.ensureCorrectSchema()

	if err := common.ValidateQualifiedIdentifier(tableName); err != nil {
		return fmt.Errorf("invalid table name: %w", err)
	}

	if len(data) == 0 {
		return fmt.Errorf("no data provided")
	}

	columns := []string{}
	placeholders := []string{}
	args := []any{}

	n := 1
	for col, val := range data {
		if err := common.ValidateIdentifier(col); err != nil {
			return fmt.Errorf("invalid column name: %w", err)
		}
		columns = append(columns, s.quote(col))
		placeholders = append(placeholders, s.placeholder(n))
		if val == nil {
			args = append(args, nil)
		} else {
			args = append(args, val)
		}
		n++
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		s.quote(tableName),
		strings.Join(columns, ", "),
		strings.Join(placeholders, ", "))

	return s.adapter.ExecuteDMLWithArgs(s.ctx, query, args...)
}

func (s *Service) DeleteRow(tableName, rowID string) error {
	if err := common.ValidateQualifiedIdentifier(tableName); err != nil {
		return fmt.Errorf("invalid table name: %w", err)
	}

	schema, err := s.adapter.GetTableColumns(s.ctx, tableName)
	if err != nil {
		query := fmt.Sprintf("DELETE FROM %s WHERE id = %s", s.quote(tableName), s.placeholder(1))
		return s.adapter.ExecuteDMLWithArgs(s.ctx, query, rowID)
	}

	pkColumn := "id"
	for _, col := range schema {
		if col.IsPrimary {
			pkColumn = col.Name
			break
		}
	}

	query := fmt.Sprintf("DELETE FROM %s WHERE %s = %s",
		s.quote(tableName), s.quote(pkColumn), s.placeholder(1))
	return s.adapter.ExecuteDMLWithArgs(s.ctx, query, rowID)
}

func (s *Service) getFilteredRowCount(tableName string, whereClause string, args []any) (int, error) {
	if whereClause == "" {
		return s.adapter.GetTableRowCount(s.ctx, tableName)
	}

	query := fmt.Sprintf("SELECT COUNT(*) as count FROM %s WHERE %s",
		s.quote(tableName), whereClause)

	result, err := s.adapter.ExecuteQueryWithArgs(s.ctx, query, args...)
	if err != nil {
		return 0, err
	}

	if len(result.Rows) > 0 {
		if count, ok := result.Rows[0]["count"]; ok {
			switch v := count.(type) {
			case int64:
				return int(v), nil
			case int:
				return v, nil
			case float64:
				return int(v), nil
			}
		}
	}

	return 0, nil
}

func (s *Service) buildWhereClause(filters []common.Filter, columnTypes map[string]string) (string, []any) {
	if len(filters) == 0 {
		return "", nil
	}

	var conditions []string
	var currentGroup []string
	var allArgs []any
	argOffset := 1 // PostgreSQL placeholders are 1-based ($1, $2, ...)

	for i, filter := range filters {
		if filter.Column == "" {
			continue
		}

		result := s.buildFilterCondition(filter, columnTypes, argOffset)
		if result == nil {
			continue
		}

		argOffset += len(result.args)
		allArgs = append(allArgs, result.args...)

		if i == 0 || filter.Logic == "where" {
			currentGroup = append(currentGroup, result.clause)
		} else if filter.Logic == "and" {
			currentGroup = append(currentGroup, result.clause)
		} else if filter.Logic == "or" {
			if len(currentGroup) > 0 {
				conditions = append(conditions, "("+strings.Join(currentGroup, " AND ")+")")
				currentGroup = []string{result.clause}
			} else {
				currentGroup = append(currentGroup, result.clause)
			}
		}
	}

	if len(currentGroup) > 0 {
		conditions = append(conditions, "("+strings.Join(currentGroup, " AND ")+")")
	}

	if len(conditions) == 0 {
		return "", nil
	}

	return strings.Join(conditions, " OR "), allArgs
}

type filterResult struct {
	clause string
	args   []any
}

func (s *Service) buildFilterCondition(filter common.Filter, columnTypes map[string]string, argOffset int) *filterResult {
	if filter.Column == "" {
		return nil
	}

	col := s.quote(filter.Column)

	colType := strings.ToLower(columnTypes[filter.Column])
	isNumeric := strings.Contains(colType, "int") || strings.Contains(colType, "serial") ||
		strings.Contains(colType, "decimal") || strings.Contains(colType, "numeric") ||
		strings.Contains(colType, "float") || strings.Contains(colType, "double") ||
		strings.Contains(colType, "real") || strings.Contains(colType, "money")

	switch filter.Operator {
	case "equals":
		if isNumeric {
			return &filterResult{
				clause: fmt.Sprintf("%s = %s", col, s.placeholder(argOffset)),
				args:   []any{filter.Value},
			}
		}
		return &filterResult{
			clause: fmt.Sprintf("LOWER(CAST(%s AS TEXT)) = LOWER(%s)", col, s.placeholder(argOffset)),
			args:   []any{filter.Value},
		}
	case "not_equals":
		if isNumeric {
			return &filterResult{
				clause: fmt.Sprintf("%s != %s", col, s.placeholder(argOffset)),
				args:   []any{filter.Value},
			}
		}
		return &filterResult{
			clause: fmt.Sprintf("LOWER(CAST(%s AS TEXT)) != LOWER(%s)", col, s.placeholder(argOffset)),
			args:   []any{filter.Value},
		}
	case "contains":
		return &filterResult{
			clause: fmt.Sprintf("LOWER(CAST(%s AS TEXT)) LIKE LOWER(%s)", col, s.placeholder(argOffset)),
			args:   []any{"%" + filter.Value + "%"},
		}
	case "not_contains":
		return &filterResult{
			clause: fmt.Sprintf("LOWER(CAST(%s AS TEXT)) NOT LIKE LOWER(%s)", col, s.placeholder(argOffset)),
			args:   []any{"%" + filter.Value + "%"},
		}
	case "starts_with":
		return &filterResult{
			clause: fmt.Sprintf("LOWER(CAST(%s AS TEXT)) LIKE LOWER(%s)", col, s.placeholder(argOffset)),
			args:   []any{filter.Value + "%"},
		}
	case "ends_with":
		return &filterResult{
			clause: fmt.Sprintf("LOWER(CAST(%s AS TEXT)) LIKE LOWER(%s)", col, s.placeholder(argOffset)),
			args:   []any{"%" + filter.Value},
		}
	case "gt":
		if isNumeric {
			return &filterResult{
				clause: fmt.Sprintf("%s > %s", col, s.placeholder(argOffset)),
				args:   []any{filter.Value},
			}
		}
		return &filterResult{
			clause: fmt.Sprintf("%s > %s", col, s.placeholder(argOffset)),
			args:   []any{filter.Value},
		}
	case "lt":
		if isNumeric {
			return &filterResult{
				clause: fmt.Sprintf("%s < %s", col, s.placeholder(argOffset)),
				args:   []any{filter.Value},
			}
		}
		return &filterResult{
			clause: fmt.Sprintf("%s < %s", col, s.placeholder(argOffset)),
			args:   []any{filter.Value},
		}
	case "gte":
		if isNumeric {
			return &filterResult{
				clause: fmt.Sprintf("%s >= %s", col, s.placeholder(argOffset)),
				args:   []any{filter.Value},
			}
		}
		return &filterResult{
			clause: fmt.Sprintf("%s >= %s", col, s.placeholder(argOffset)),
			args:   []any{filter.Value},
		}
	case "lte":
		if isNumeric {
			return &filterResult{
				clause: fmt.Sprintf("%s <= %s", col, s.placeholder(argOffset)),
				args:   []any{filter.Value},
			}
		}
		return &filterResult{
			clause: fmt.Sprintf("%s <= %s", col, s.placeholder(argOffset)),
			args:   []any{filter.Value},
		}
	case "is_null":
		return &filterResult{
			clause: fmt.Sprintf("%s IS NULL", col),
			args:   nil,
		}
	case "is_not_null":
		return &filterResult{
			clause: fmt.Sprintf("%s IS NOT NULL", col),
			args:   nil,
		}
	case "is_empty":
		return &filterResult{
			clause: fmt.Sprintf("(%s IS NULL OR CAST(%s AS TEXT) = '')", col, col),
			args:   nil,
		}
	case "is_not_empty":
		return &filterResult{
			clause: fmt.Sprintf("(%s IS NOT NULL AND CAST(%s AS TEXT) != '')", col, col),
			args:   nil,
		}
	default:
		return nil
	}
}

func (s *Service) getRowsFiltered(tableName string, limit, offset int, whereClause string, args []any) ([]map[string]any, error) {
	// ScyllaDB does not support LIMIT/OFFSET in CQL the same way as SQL.
	// Use GetTableData which returns all rows, then paginate in-memory.
	if s.adapter.ProviderName() == "scylla" || s.adapter.ProviderName() == "scylladb" || s.adapter.ProviderName() == "cassandra" {
		data, err := s.adapter.GetTableData(s.ctx, tableName)
		if err != nil {
			return nil, err
		}
		start := offset
		end := offset + limit
		if start > len(data) {
			return []map[string]any{}, nil
		}
		if end > len(data) {
			end = len(data)
		}
		return data[start:end], nil
	}

	var query string
	if whereClause != "" {
		query = fmt.Sprintf("SELECT * FROM %s WHERE %s LIMIT %d OFFSET %d",
			s.quote(tableName), whereClause, limit, offset)
	} else {
		// Try to use paginated query first (only when no filter)
		type PaginatedFetcher interface {
			GetTableDataPaginated(ctx context.Context, tableName string, limit, offset int) ([]map[string]any, error)
		}

		if fetcher, ok := s.adapter.(PaginatedFetcher); ok {
			return fetcher.GetTableDataPaginated(s.ctx, tableName, limit, offset)
		}

		query = fmt.Sprintf("SELECT * FROM %s LIMIT %d OFFSET %d",
			s.quote(tableName), limit, offset)
	}

	var rows []map[string]any
	if whereClause != "" {
		res, err := s.adapter.ExecuteQueryWithArgs(s.ctx, query, args...)
		if err != nil {
			data, err2 := s.adapter.GetTableData(s.ctx, tableName)
			if err2 != nil {
				return nil, err
			}
			start := offset
			end := offset + limit
			if start > len(data) {
				return []map[string]any{}, nil
			}
			if end > len(data) {
				end = len(data)
			}
			return data[start:end], nil
		}
		rows = res.Rows
	} else {
		res, err := s.adapter.ExecuteQuery(s.ctx, query)
		if err != nil {
			data, err2 := s.adapter.GetTableData(s.ctx, tableName)
			if err2 != nil {
				return nil, err
			}
			start := offset
			end := offset + limit
			if start > len(data) {
				return []map[string]any{}, nil
			}
			if end > len(data) {
				end = len(data)
			}
			return data[start:end], nil
		}
		rows = res.Rows
	}

	return rows, nil
}

func (s *Service) GetSchemaVisualization() (map[string]any, error) {
	_ = s.ensureCorrectSchema()

	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	type SchemaFetcher interface {
		PullCompleteSchema(ctx context.Context) ([]types.SchemaTable, error)
	}

	var allTables []types.SchemaTable

	// ScyllaDB/Cassandra — iterate all user keyspaces
	if s.adapter.ProviderName() == "scylla" || s.adapter.ProviderName() == "scylladb" || s.adapter.ProviderName() == "cassandra" {
		type SchemaSwitcher interface {
			SetActiveSchema(ctx context.Context, schema string) error
		}
		keyspaces, err := s.adapter.GetKeyspaces(s.ctx)
		if err != nil {
			return nil, err
		}
		for _, ks := range keyspaces {
			if ks == "system" || ks == "system_schema" || ks == "system_auth" || ks == "system_distributed" || ks == "system_traces" {
				continue
			}
			if sw, ok := s.adapter.(SchemaSwitcher); ok {
				_ = sw.SetActiveSchema(ctx, ks)
			}
			if fetcher, ok := s.adapter.(SchemaFetcher); ok {
				tbls, _ := fetcher.PullCompleteSchema(ctx)
				for i := range tbls {
					tbls[i].Name = ks + "." + tbls[i].Name
				}
				allTables = append(allTables, tbls...)
			}
		}
	} else {
		var err error
		if fetcher, ok := s.adapter.(SchemaFetcher); ok {
			allTables, err = fetcher.PullCompleteSchema(ctx)
		} else {
			allTables, err = s.adapter.GetCurrentSchema(ctx)
		}
		if err != nil {
			return nil, err
		}
	}

	enums, _ := s.adapter.GetCurrentEnums(ctx)
	_ = enums

	tables := allTables
	nodes := make([]map[string]any, 0, len(tables))
	nodeIndex := make(map[string]string, len(tables))

	batchSize := 10
	for i := 0; i < len(tables); i += batchSize {
		end := min(i+batchSize, len(tables))

		// Process batch
		for j := i; j < end; j++ {
			table := tables[j]
			nodeID := fmt.Sprintf("table-%d", j)
			nodeIndex[table.Name] = nodeID

			columns := make([]map[string]any, 0, len(table.Columns))
			columnMap := make(map[string]bool, len(table.Columns))

			for _, col := range table.Columns {
				if !columnMap[col.Name] {
					columnMap[col.Name] = true
					columns = append(columns, map[string]any{
						"name":             col.Name,
						"type":             col.Type,
						"isPrimary":        col.IsPrimary,
						"isForeign":        col.ForeignKeyTable != "",
						"nullable":         col.Nullable,
						"default":          col.Default,
						"foreignKeyTable":  col.ForeignKeyTable,
						"foreignKeyColumn": col.ForeignKeyColumn,
						"isUnique":         col.IsUnique,
						"isAutoIncrement":  col.IsAutoIncrement,
					})
				}
			}

			nodes = append(nodes, map[string]any{
				"id": nodeID,
				"data": map[string]any{
					"label":    table.Name,
					"columns":  columns,
					"keyspace": extractKeyspace(table.Name),
				},
				"position": map[string]int{
					"x": 100 + (j%4)*300,
					"y": 100 + (j/4)*250,
				},
			})
		}
	}

	edges := make([]map[string]any, 0)
	edgeMap := make(map[string]bool)

	for _, table := range tables {
		sourceID := nodeIndex[table.Name]
		for _, col := range table.Columns {
			if col.ForeignKeyTable != "" {
				if targetID, ok := nodeIndex[col.ForeignKeyTable]; ok {
					edgeID := fmt.Sprintf("%s-%s-%s", sourceID, targetID, col.Name)

					if !edgeMap[edgeID] {
						edgeMap[edgeID] = true

						// Use the actual FK target column if available, otherwise find PK
						targetColumn := col.ForeignKeyColumn
						if targetColumn == "" {
							for _, targetTable := range tables {
								if targetTable.Name == col.ForeignKeyTable {
									for _, targetCol := range targetTable.Columns {
										if targetCol.IsPrimary {
											targetColumn = targetCol.Name
											break
										}
									}
									break
								}
							}
						}

						edges = append(edges, map[string]any{
							"id":           edgeID,
							"source":       sourceID,
							"target":       targetID,
							"label":        col.Name,
							"sourceHandle": col.Name,
							"targetHandle": targetColumn,
						})
					}
				}
			}
		}
	}

	return map[string]any{"nodes": nodes, "edges": edges, "enums": enums}, nil
}

// stripSQLComments removes leading SQL comments (-- line comments and /* block comments */)
// so that query type detection works correctly even when queries start with comments.
func stripSQLComments(query string) string {
	query = strings.TrimSpace(query)
	for {
		if strings.HasPrefix(query, "--") {
			idx := strings.Index(query, "\n")
			if idx >= 0 {
				query = strings.TrimSpace(query[idx+1:])
			} else {
				return ""
			}
			continue
		}
		if strings.HasPrefix(query, "#") {
			idx := strings.Index(query, "\n")
			if idx >= 0 {
				query = strings.TrimSpace(query[idx+1:])
			} else {
				return ""
			}
			continue
		}
		if strings.HasPrefix(query, "/*") {
			idx := strings.Index(query, "*/")
			if idx >= 0 {
				query = strings.TrimSpace(query[idx+2:])
			} else {
				return ""
			}
			continue
		}
		break
	}
	return query
}

// splitSQLStatements splits a multi-statement SQL string on top-level semicolons,
// stripping leading comments from each statement.
func splitSQLStatements(sql string) []string {
	var stmts []string
	var cur strings.Builder
	inSingle := false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if inSingle {
			cur.WriteByte(c)
			if c == '\'' {
				if i+1 < len(sql) && sql[i+1] == '\'' {
					cur.WriteByte(sql[i+1])
					i++
				} else {
					inSingle = false
				}
			}
			continue
		}
		if c == '\'' {
			inSingle = true
			cur.WriteByte(c)
			continue
		}
		// Skip line comments
		if c == '-' && i+1 < len(sql) && sql[i+1] == '-' {
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			cur.WriteByte('\n')
			continue
		}
		if c == ';' {
			s := strings.TrimSpace(stripSQLComments(cur.String()))
			if s != "" {
				stmts = append(stmts, s)
			}
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	if s := strings.TrimSpace(stripSQLComments(cur.String())); s != "" {
		stmts = append(stmts, s)
	}
	return stmts
}

func (s *Service) ExecuteSQL(query string) (*common.TableData, error) {
	_ = s.ensureCorrectSchema()
	query = strings.TrimSpace(query)

	// Split into individual statements and execute sequentially.
	// Return the result of the last SELECT-type statement (or last DML result).
	stmts := splitSQLStatements(query)
	if len(stmts) > 1 {
		var lastResult *common.TableData
		var lastErr error
		for _, stmt := range stmts {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			res, err := s.ExecuteSQL(stmt)
			if err != nil {
				return nil, err
			}
			lastResult = res
			lastErr = err
		}
		_ = lastErr
		if lastResult != nil {
			return lastResult, nil
		}
	}

	// Strip leading comments to detect the actual query type
	queryForDetection := stripSQLComments(query)
	queryUpper := strings.ToUpper(queryForDetection)

	// Handle USE <keyspace> for ScyllaDB/Cassandra/ClickHouse/MySQL
	if strings.HasPrefix(queryUpper, "USE ") {
		// Extract only the keyspace name — stop at first whitespace or semicolon
		rest := strings.TrimSpace(queryForDetection[4:])
		ksName := strings.FieldsFunc(rest, func(r rune) bool {
			return r == ';' || r == ' ' || r == '\t' || r == '\n'
		})
		if len(ksName) == 0 {
			return nil, fmt.Errorf("USE requires a keyspace name")
		}
		ks := strings.Trim(ksName[0], `"'`+"`")
		provider := ""
		if s.cfg != nil {
			provider = s.cfg.Database.Provider
		}
		switch provider {
		case "scylla", "scylladb", "cassandra":
			type SchemaSwitcher interface {
				SetActiveSchema(ctx context.Context, schema string) error
			}
			if sw, ok := s.adapter.(SchemaSwitcher); ok {
				if err := sw.SetActiveSchema(s.ctx, ks); err != nil {
					return nil, fmt.Errorf("USE failed: %w", err)
				}
			}
		case "clickhouse", "mysql":
			if _, err := s.adapter.ExecuteQuery(s.ctx, "USE "+ks); err != nil {
				return nil, fmt.Errorf("USE failed: %w", err)
			}
		}
		return &common.TableData{
			Columns: []common.ColumnInfo{{Name: "result", Type: "TEXT"}},
			Rows:    []map[string]any{{"result": "Switched to keyspace: " + ks}},
			Total:   1, Page: 1, Limit: 1,
		}, nil
	}

	// Detect query type more comprehensively
	// For CQL: qualify unqualified table refs using active keyspace
	provider := ""
	if s.cfg != nil {
		provider = s.cfg.Database.Provider
	}
	qualifiedQuery := query
	if provider == "scylla" || provider == "scylladb" || provider == "cassandra" {
		type KSGetter interface{ CurrentKeyspace() string }
		if kg, ok := s.adapter.(KSGetter); ok {
			if ks := kg.CurrentKeyspace(); ks != "" && !isSystemKeyspace(ks) {
				qualifiedQuery = qualifyCQLQuery(query, ks)
			}
		}
	}

	isSelectQuery := strings.HasPrefix(queryUpper, "SELECT") ||
		strings.HasPrefix(queryUpper, "SHOW") ||
		strings.HasPrefix(queryUpper, "DESCRIBE") ||
		strings.HasPrefix(queryUpper, "EXPLAIN") ||
		strings.HasPrefix(queryUpper, "WITH") ||
		strings.HasPrefix(queryUpper, "TABLE") ||
		strings.HasPrefix(queryUpper, "VALUES")

	// Handle SET statements - they may or may not return data depending on database
	isSetStatement := strings.HasPrefix(queryUpper, "SET")

	if isSelectQuery {
		result, err := s.adapter.ExecuteQuery(s.ctx, qualifiedQuery)
		if err != nil {
			return nil, fmt.Errorf("query execution failed: %w", err)
		}

		columns := make([]common.ColumnInfo, len(result.Columns))
		for i, col := range result.Columns {
			columns[i] = common.ColumnInfo{Name: col, Type: "TEXT"}
		}

		return &common.TableData{
			Columns: columns,
			Rows:    result.Rows,
			Total:   len(result.Rows),
			Page:    1,
			Limit:   len(result.Rows),
		}, nil
	}

	if isSetStatement {
		result, err := s.adapter.ExecuteQuery(s.ctx, query)
		if err == nil && result != nil {
			columns := make([]common.ColumnInfo, len(result.Columns))
			for i, col := range result.Columns {
				columns[i] = common.ColumnInfo{Name: col, Type: "TEXT"}
			}
			return &common.TableData{
				Columns: columns,
				Rows:    result.Rows,
				Total:   len(result.Rows),
				Page:    1,
				Limit:   len(result.Rows),
			}, nil
		}
	}

	if err := s.adapter.ExecuteMigration(s.ctx, qualifiedQuery); err != nil {
		return nil, fmt.Errorf("query execution failed: %w", err)
	}

	return &common.TableData{
		Columns: []common.ColumnInfo{},
		Rows:    []map[string]any{},
		Total:   0,
		Page:    1,
		Limit:   0,
	}, nil
}

func extractKeyspace(name string) string {
	if before, _, ok := strings.Cut(name, "."); ok {
		return before
	}
	return ""
}

// qualifyCQLQuery rewrites unqualified table references to use the given keyspace.
// Only rewrites when table name is NOT already keyspace-qualified (not followed by a dot).
func qualifyCQLQuery(query, ks string) string {
	re := regexp.MustCompile(`(?i)\b(FROM|INTO|UPDATE|JOIN)\s+([a-zA-Z_]\w*)`)
	matches := re.FindAllStringSubmatchIndex(query, -1)
	if len(matches) == 0 {
		return query
	}
	var out strings.Builder
	prev := 0
	for _, m := range matches {
		// m[0]:m[1] = full match, m[2]:m[3] = keyword, m[4]:m[5] = table name
		matchEnd := m[1]
		// If next char after match is '.', table is already ks.qualified — skip
		if matchEnd < len(query) && query[matchEnd] == '.' {
			continue
		}
		kw := query[m[2]:m[3]]
		tbl := query[m[4]:m[5]]
		out.WriteString(query[prev:m[0]])
		out.WriteString(kw + " " + ks + "." + tbl)
		prev = matchEnd
	}
	out.WriteString(query[prev:])
	return out.String()
}

func (s *Service) UpdateRow(table string, id any, data map[string]any) error {
	_ = s.ensureCorrectSchema()

	if err := common.ValidateIdentifier(table); err != nil {
		return fmt.Errorf("invalid table name: %w", err)
	}

	schema, err := s.adapter.GetTableColumns(s.ctx, table)
	if err != nil {
		return err
	}

	pkColumn := "id"
	for _, col := range schema {
		if col.IsPrimary {
			pkColumn = col.Name
			break
		}
	}

	var setClauses []string
	var args []any
	n := 1
	for col, val := range data {
		if err := common.ValidateIdentifier(col); err != nil {
			return fmt.Errorf("invalid column name: %w", err)
		}
		if val == nil {
			setClauses = append(setClauses, fmt.Sprintf("%s = NULL", s.quote(col)))
		} else {
			setClauses = append(setClauses, fmt.Sprintf("%s = %s", s.quote(col), s.placeholder(n)))
			args = append(args, val)
			n++
		}
	}

	idStr := fmt.Sprintf("%v", id)
	query := fmt.Sprintf("UPDATE %s SET %s WHERE %s = %s",
		s.quote(table), strings.Join(setClauses, ", "),
		s.quote(pkColumn), s.placeholder(n))
	args = append(args, idStr)

	return s.adapter.ExecuteDMLWithArgs(s.ctx, query, args...)
}

func (s *Service) InsertRow(table string, data map[string]any) error {
	_ = s.ensureCorrectSchema()

	if err := common.ValidateIdentifier(table); err != nil {
		return fmt.Errorf("invalid table name: %w", err)
	}

	if len(data) == 0 {
		return fmt.Errorf("no data provided")
	}

	var columns []string
	var placeholders []string
	var args []any
	n := 1
	for col, val := range data {
		if err := common.ValidateIdentifier(col); err != nil {
			return fmt.Errorf("invalid column name: %w", err)
		}
		columns = append(columns, s.quote(col))
		placeholders = append(placeholders, s.placeholder(n))
		if val == nil {
			args = append(args, nil)
		} else {
			args = append(args, val)
		}
		n++
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		s.quote(table), strings.Join(columns, ", "), strings.Join(placeholders, ", "))

	return s.adapter.ExecuteDMLWithArgs(s.ctx, query, args...)
}

func (s *Service) GetBranches() ([]map[string]any, string, error) {
	if s.cfg == nil {
		return nil, "", fmt.Errorf("no config loaded")
	}

	manager, err := branch.NewManager(s.cfg)
	if err != nil {
		return nil, "", err
	}
	defer manager.Close()

	branches, current, err := manager.ListBranches()
	if err != nil {
		return nil, "", err
	}

	result := make([]map[string]any, len(branches))
	for i, b := range branches {
		result[i] = map[string]any{
			"name":       b.Name,
			"parent":     b.Parent,
			"schema":     b.Schema,
			"created_at": b.CreatedAt,
			"is_default": b.IsDefault,
		}
	}

	return result, current, nil
}

func (s *Service) SwitchBranch(branchName string) error {
	if s.cfg == nil {
		return fmt.Errorf("no config loaded")
	}

	manager, err := branch.NewManager(s.cfg)
	if err != nil {
		return err
	}
	defer manager.Close()

	ctx := context.Background()
	if err := manager.SwitchBranch(ctx, branchName); err != nil {
		return err
	}

	branchSchema, err := manager.GetBranchSchema(branchName)
	if err != nil {
		return err
	}

	switch s.cfg.Database.Provider {
	case "postgresql", "postgres":
		query := fmt.Sprintf("SET search_path TO %s, public", s.quote(branchSchema))
		if _, err := s.adapter.ExecuteQuery(ctx, query); err != nil {
			return fmt.Errorf("failed to set search_path: %w", err)
		}
	case "clickhouse":
		query := fmt.Sprintf("USE %s", s.quote(branchSchema))
		if _, err := s.adapter.ExecuteQuery(ctx, query); err != nil {
			return fmt.Errorf("failed to switch database: %w", err)
		}
	case "scylla", "scylladb", "cassandra":
		if err := s.adapter.SetActiveSchema(ctx, branchSchema); err != nil {
			return fmt.Errorf("failed to switch keyspace: %w", err)
		}
	case "mysql", "sqlite", "sqlite3":
		type DatabaseSwitcher interface {
			SwitchDatabase(ctx context.Context, dbName string) error
		}
		if switcher, ok := s.adapter.(DatabaseSwitcher); ok {
			if err := switcher.SwitchDatabase(ctx, branchSchema); err != nil {
				return fmt.Errorf("failed to switch database: %w", err)
			}
		}
	}

	return nil
}

// GetEditorHints returns schema information optimized for editor autocomplete
// This data should be cached on the client side to avoid repeated database calls
func (s *Service) GetEditorHints() (map[string]any, error) {
	_ = s.ensureCorrectSchema()

	provider := "sql"
	if s.cfg != nil {
		provider = s.cfg.Database.Provider
	}

	schema := make(map[string][]map[string]string)

	// Use PullCompleteSchema for a single bulk query instead of N per-table calls
	type SchemaFetcher interface {
		PullCompleteSchema(ctx context.Context) ([]types.SchemaTable, error)
	}
	type KSGetter interface{ CurrentKeyspace() string }

	if fetcher, ok := s.adapter.(SchemaFetcher); ok {
		if schemaTables, err := fetcher.PullCompleteSchema(s.ctx); err == nil {
			currentKS := ""
			if kg, ok2 := s.adapter.(KSGetter); ok2 {
				currentKS = kg.CurrentKeyspace()
				if currentKS == "system" || isSystemKeyspace(currentKS) {
					currentKS = ""
				}
			}

			isCQL := provider == "scylla" || provider == "scylladb" || provider == "cassandra"

			// For CQL: fetch columns for ALL user keyspaces from system_schema
			if isCQL {
				if allKS, err2 := s.adapter.GetKeyspaces(s.ctx); err2 == nil {
					for _, ks := range allKS {
						if isSystemKeyspace(ks) {
							continue
						}
						r, err3 := s.adapter.ExecuteQuery(s.ctx,
							fmt.Sprintf(`SELECT table_name, column_name, type FROM system_schema.columns WHERE keyspace_name = '%s' ALLOW FILTERING`, ks))
						if err3 != nil {
							continue
						}
						tblMap := make(map[string][]map[string]string)
						for _, row := range r.Rows {
							tbl := toString(row["table_name"])
							if tbl == "_flash_migrations" {
								continue
							}
							tblMap[tbl] = append(tblMap[tbl], map[string]string{
								"name": toString(row["column_name"]),
								"type": toString(row["type"]),
							})
						}
						for tbl, cols := range tblMap {
							// Only store as ks.table — no plain name to avoid cross-keyspace collisions
							schema[ks+"."+tbl] = cols
						}
					}
				}
				// Collect user keyspaces from schema keys
				var keyspaces []string
				ksSet := make(map[string]bool)
				for key := range schema {
					if before, _, ok := strings.Cut(key, "."); ok {
						ks := before
						if !ksSet[ks] {
							ksSet[ks] = true
							keyspaces = append(keyspaces, ks)
						}
					}
				}
				return map[string]any{"provider": provider, "schema": schema, "keyspaces": keyspaces, "currentKeyspace": currentKS}, nil
			}

			// Non-CQL: use PullCompleteSchema result
			for _, t := range schemaTables {
				if t.Name == "_flash_migrations" {
					continue
				}
				cols := make([]map[string]string, 0, len(t.Columns))
				seen := make(map[string]bool, len(t.Columns))
				for _, col := range t.Columns {
					if seen[col.Name] {
						continue
					}
					seen[col.Name] = true
					cols = append(cols, map[string]string{"name": col.Name, "type": col.Type})
				}
				schema[t.Name] = cols
			}
			return map[string]any{"provider": provider, "schema": schema, "keyspaces": []string{}, "currentKeyspace": ""}, nil
		}
	}

	// Fallback: parallel per-table column queries
	tables, err := s.adapter.GetAllTableNames(s.ctx)
	if err != nil {
		return nil, err
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, tableName := range tables {
		if tableName == "_flash_migrations" {
			continue
		}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			columns, err := s.adapter.GetTableColumns(s.ctx, name)
			cols := []map[string]string{}
			if err == nil {
				seen := make(map[string]bool)
				for _, col := range columns {
					if seen[col.Name] {
						continue
					}
					seen[col.Name] = true
					cols = append(cols, map[string]string{"name": col.Name, "type": col.Type})
				}
			}
			mu.Lock()
			schema[name] = cols
			if _, after, ok := strings.Cut(name, "."); ok {
				plainName := after
				if _, exists := schema[plainName]; !exists {
					schema[plainName] = cols
				}
			}
			mu.Unlock()
		}(tableName)
	}
	wg.Wait()

	var keyspaces []string
	if provider == "scylla" || provider == "scylladb" || provider == "cassandra" {
		ksSet := make(map[string]bool)
		for name := range schema {
			if before, _, ok := strings.Cut(name, "."); ok {
				ks := before
				if !ksSet[ks] {
					ksSet[ks] = true
					keyspaces = append(keyspaces, ks)
				}
			}
		}
	}

	return map[string]any{"provider": provider, "schema": schema, "keyspaces": keyspaces}, nil
}

// sortTablesByDependency sorts tables in topological order based on foreign key dependencies.
// colsByTable is a pre-fetched map of table -> columns; pass nil to fall back to per-table queries.
func (s *Service) sortTablesByDependency(ctx context.Context, tables []string, colsByTable map[string][]types.SchemaColumn) ([]string, error) {
	dependencies := make(map[string][]string)
	for _, t := range tables {
		dependencies[t] = []string{}
	}

	// Build FK dependencies from cached columns or per-table queries
	for _, tableName := range tables {
		var cols []types.SchemaColumn
		if colsByTable != nil {
			cols = colsByTable[tableName]
		} else {
			var err error
			cols, err = s.adapter.GetTableColumns(ctx, tableName)
			if err != nil {
				continue
			}
		}
		for _, col := range cols {
			if col.ForeignKeyTable != "" {
				dependencies[tableName] = append(dependencies[tableName], col.ForeignKeyTable)
			}
		}
	}

	// Kahn's algorithm for topological sort
	inDegree := make(map[string]int)
	for _, t := range tables {
		inDegree[t] = 0
	}

	// Count incoming edges (how many tables reference this table)
	for _, deps := range dependencies {
		for _, dep := range deps {
			if _, exists := inDegree[dep]; exists {
				inDegree[dep]++ // This is reversed - we want tables with no dependencies first
			}
		}
	}

	// Reset and calculate properly
	for _, t := range tables {
		inDegree[t] = len(dependencies[t])
	}

	// Queue tables with no dependencies
	var queue []string
	for _, t := range tables {
		if inDegree[t] == 0 {
			queue = append(queue, t)
		}
	}

	var sorted []string
	for len(queue) > 0 {
		// Pop from queue
		current := queue[0]
		queue = queue[1:]
		sorted = append(sorted, current)

		// For each table that depends on current, reduce its in-degree
		for t, deps := range dependencies {
			for _, dep := range deps {
				if dep == current {
					inDegree[t]--
					if inDegree[t] == 0 {
						queue = append(queue, t)
					}
				}
			}
		}
	}

	// If we couldn't sort all tables (circular dependency), add remaining
	if len(sorted) < len(tables) {
		for _, t := range tables {
			found := slices.Contains(sorted, t)
			if !found {
				sorted = append(sorted, t)
			}
		}
	}

	return sorted, nil
}

// getEnumTypes retrieves all custom ENUM types from PostgreSQL
func (s *Service) getEnumTypes(ctx context.Context) ([]common.ExportEnumType, error) {
	// This query works for PostgreSQL to get all enum types and their values
	query := `
		SELECT t.typname as enum_name,
		       array_agg(e.enumlabel ORDER BY e.enumsortorder) as enum_values
		FROM pg_type t
		JOIN pg_enum e ON t.oid = e.enumtypid
		JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
		WHERE n.nspname = 'public'
		GROUP BY t.typname
		ORDER BY t.typname
	`

	result, err := s.adapter.ExecuteQuery(ctx, query)
	if err != nil {
		// Not PostgreSQL or no enums - return empty
		return []common.ExportEnumType{}, nil
	}

	var enumTypes []common.ExportEnumType
	for _, row := range result.Rows {
		enumName, ok := row["enum_name"].(string)
		if !ok {
			continue
		}

		var values []string
		// Handle the array of enum values
		if enumValues, ok := row["enum_values"].([]any); ok {
			for _, v := range enumValues {
				if str, ok := v.(string); ok {
					values = append(values, str)
				}
			}
		} else if enumValuesStr, ok := row["enum_values"].(string); ok {
			// PostgreSQL may return as string like {val1,val2,val3}
			enumValuesStr = strings.Trim(enumValuesStr, "{}")
			if enumValuesStr != "" {
				values = strings.Split(enumValuesStr, ",")
			}
		}

		if len(values) > 0 {
			enumTypes = append(enumTypes, common.ExportEnumType{
				Name:   enumName,
				Values: values,
			})
		}
	}

	return enumTypes, nil
}

// ExportDatabase exports the database schema and/or data based on export type
func (s *Service) ExportDatabase(exportType common.ExportType) (*common.ExportData, error) {
	_ = s.ensureCorrectSchema()

	ctx, cancel := context.WithTimeout(s.ctx, 60*time.Second)
	defer cancel()

	provider := "sql"
	if s.cfg != nil {
		provider = s.cfg.Database.Provider
	}

	var tableNames []string
	var schemaTables []types.SchemaTable

	if exportType == common.ExportSchemaOnly || exportType == common.ExportComplete {
		var err error
		schemaTables, err = s.adapter.PullCompleteSchema(ctx)
		if err != nil {
			schemaTables, _ = s.adapter.GetCurrentSchema(ctx)
		}
		for _, t := range schemaTables {
			tableNames = append(tableNames, t.Name)
		}
	}

	if len(tableNames) == 0 {
		var err error
		tableNames, err = s.adapter.GetAllTableNames(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get tables: %w", err)
		}
	}

	// Build table → schema lookup for quick access
	schemaMap := make(map[string]*types.SchemaTable, len(schemaTables))
	for i := range schemaTables {
		t := &schemaTables[i]
		schemaMap[t.Name] = t
	}

	sortedTables, err := s.sortTablesByDependency(ctx, tableNames, nil)
	if err != nil {
		sortedTables = tableNames
	}

	exportData := &common.ExportData{
		Version:          "1.0",
		ExportedAt:       time.Now().UTC().Format(time.RFC3339),
		DatabaseProvider: provider,
		ExportType:       exportType,
		Tables:           make([]common.ExportTable, 0),
	}

	// Export ENUM types for schema exports (PostgreSQL only)
	if provider != "scylla" && provider != "scylladb" && provider != "cassandra" {
		if exportType == common.ExportSchemaOnly || exportType == common.ExportComplete {
			enumTypes, err := s.getEnumTypes(ctx)
			if err == nil && len(enumTypes) > 0 {
				exportData.EnumTypes = enumTypes
			}
		}
	}

	for _, tableName := range sortedTables {
		if tableName == "_flash_migrations" {
			continue
		}

		exportTable := common.ExportTable{
			Name: tableName,
		}

		if exportType == common.ExportSchemaOnly || exportType == common.ExportComplete {
			if tbl, ok := schemaMap[tableName]; ok {
				exportTable.Schema = s.buildTableSchemaFromTable(tbl)
			} else {
				schema, err := s.getTableSchema(ctx, tableName)
				if err != nil {
					return nil, fmt.Errorf("failed to get schema for table %s: %w", tableName, err)
				}
				exportTable.Schema = schema
			}
		}

		if exportType == common.ExportDataOnly || exportType == common.ExportComplete {
			data, err := s.getAllTableData(ctx, tableName)
			if err != nil {
				return nil, fmt.Errorf("failed to get data for table %s: %w", tableName, err)
			}
			exportTable.Data = data
		}

		exportData.Tables = append(exportData.Tables, exportTable)
	}

	return exportData, nil
}

// buildTableSchemaFromTable builds an ExportTableSchema from a full SchemaTable (includes indexes).
func (s *Service) buildTableSchemaFromTable(t *types.SchemaTable) *common.ExportTableSchema {
	exportColumns := make([]common.ExportColumn, 0, len(t.Columns))
	seen := make(map[string]bool, len(t.Columns))
	for _, col := range t.Columns {
		if seen[col.Name] {
			continue
		}
		seen[col.Name] = true
		exportColumns = append(exportColumns, common.ExportColumn{
			Name:             col.Name,
			Type:             col.Type,
			Nullable:         col.Nullable,
			PrimaryKey:       col.IsPrimary,
			Default:          col.Default,
			AutoIncrement:    col.IsAutoIncrement,
			Unique:           col.IsUnique,
			ForeignKeyTable:  col.ForeignKeyTable,
			ForeignKeyColumn: col.ForeignKeyColumn,
			OnDeleteAction:   col.OnDeleteAction,
			OnUpdateAction:   col.OnUpdateAction,
			Check:            col.Check,
			Generated:        col.Generated,
			IsIdentity:       col.IsIdentity,
		})
	}

	// Include indexes
	var exportIndexes []common.ExportIndex
	if len(t.Indexes) > 0 {
		exportIndexes = make([]common.ExportIndex, 0, len(t.Indexes))
		for _, idx := range t.Indexes {
			exportIndexes = append(exportIndexes, common.ExportIndex{
				Name:    idx.Name,
				Columns: idx.Columns,
				Unique:  idx.Unique,
				Where:   idx.Where,
				Method:  idx.Method,
				Expr:    idx.Expr,
			})
		}
	}

	return &common.ExportTableSchema{Columns: exportColumns, Indexes: exportIndexes}
}

// getTableSchema returns the schema for a table (fallback when PullCompleteSchema unavailable)
func (s *Service) getTableSchema(ctx context.Context, tableName string) (*common.ExportTableSchema, error) {
	columns, err := s.adapter.GetTableColumns(ctx, tableName)
	if err != nil {
		return nil, err
	}

	exportColumns := make([]common.ExportColumn, 0, len(columns))
	seen := make(map[string]bool)

	for _, col := range columns {
		if seen[col.Name] {
			continue
		}
		seen[col.Name] = true

		exportColumns = append(exportColumns, common.ExportColumn{
			Name:             col.Name,
			Type:             col.Type,
			Nullable:         col.Nullable,
			PrimaryKey:       col.IsPrimary,
			Default:          col.Default,
			AutoIncrement:    col.IsAutoIncrement,
			Unique:           col.IsUnique,
			ForeignKeyTable:  col.ForeignKeyTable,
			ForeignKeyColumn: col.ForeignKeyColumn,
			OnDeleteAction:   col.OnDeleteAction,
			OnUpdateAction:   col.OnUpdateAction,
			Check:            col.Check,
			Generated:        col.Generated,
			IsIdentity:       col.IsIdentity,
		})
	}

	return &common.ExportTableSchema{
		Columns: exportColumns,
	}, nil
}

// getAllTableData returns all data from a table.
func (s *Service) getAllTableData(ctx context.Context, tableName string) ([]map[string]any, error) {
	const batchSize = 1000
	allData := make([]map[string]any, 0, batchSize)

	for offset := 0; ; offset += batchSize {
		query := fmt.Sprintf("SELECT * FROM %s LIMIT %d OFFSET %d",
			common.QuoteIdentifier(tableName), batchSize, offset)

		result, err := s.adapter.ExecuteQuery(ctx, query)
		if err != nil {
			if offset == 0 {
				return s.adapter.GetTableData(ctx, tableName)
			}
			break
		}

		allData = append(allData, result.Rows...)

		if len(result.Rows) < batchSize {
			break
		}
	}

	return allData, nil
}

// sortImportTablesByDependency sorts import tables in topological order based on foreign key dependencies
func (s *Service) sortImportTablesByDependency(tables []common.ExportTable) []common.ExportTable {
	// Build dependency graph from schema info
	dependencies := make(map[string][]string)
	tableMap := make(map[string]common.ExportTable)

	for _, t := range tables {
		tableMap[t.Name] = t
		dependencies[t.Name] = []string{}

		if t.Schema != nil {
			for _, col := range t.Schema.Columns {
				if col.ForeignKeyTable != "" {
					dependencies[t.Name] = append(dependencies[t.Name], col.ForeignKeyTable)
				}
			}
		}
	}

	// Calculate in-degree (number of dependencies)
	inDegree := make(map[string]int)
	for _, t := range tables {
		inDegree[t.Name] = len(dependencies[t.Name])
	}

	// Queue tables with no dependencies
	var queue []string
	for _, t := range tables {
		if inDegree[t.Name] == 0 {
			queue = append(queue, t.Name)
		}
	}

	var sortedNames []string
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		sortedNames = append(sortedNames, current)

		// For each table that depends on current, reduce its in-degree
		for name, deps := range dependencies {
			for _, dep := range deps {
				if dep == current {
					inDegree[name]--
					if inDegree[name] == 0 {
						queue = append(queue, name)
					}
				}
			}
		}
	}

	// Add any remaining tables (circular dependencies)
	for _, t := range tables {
		found := slices.Contains(sortedNames, t.Name)
		if !found {
			sortedNames = append(sortedNames, t.Name)
		}
	}

	// Build sorted result
	sorted := make([]common.ExportTable, 0, len(tables))
	for _, name := range sortedNames {
		if t, ok := tableMap[name]; ok {
			sorted = append(sorted, t)
		}
	}

	return sorted
}

// getFKChecksState queries the current FK check state and returns a restore function.
// It only disables FK checks if they are currently enabled; if already disabled it's a no-op.
func (s *Service) disableFKChecksIfNeeded(ctx context.Context) (restore func()) {
	provider := ""
	if s.cfg != nil {
		provider = s.cfg.Database.Provider
	}

	switch provider {
	case "mysql":
		// Query current state
		res, err := s.adapter.ExecuteQuery(ctx, "SELECT @@FOREIGN_KEY_CHECKS AS fk")
		if err == nil && len(res.Rows) > 0 {
			val := fmt.Sprintf("%v", res.Rows[0]["fk"])
			if val == "0" {
				// Already disabled, nothing to do
				return func() {}
			}
		}
		_ = s.adapter.ExecuteMigration(ctx, "SET FOREIGN_KEY_CHECKS = 0")
		return func() {
			_ = s.adapter.ExecuteMigration(ctx, "SET FOREIGN_KEY_CHECKS = 1")
		}

	case "sqlite", "sqlite3":
		res, err := s.adapter.ExecuteQuery(ctx, "PRAGMA foreign_keys")
		if err == nil && len(res.Rows) > 0 {
			for _, v := range res.Rows[0] {
				if fmt.Sprintf("%v", v) == "0" {
					return func() {}
				}
			}
		}
		_ = s.adapter.ExecuteMigration(ctx, "PRAGMA foreign_keys = OFF")
		return func() {
			_ = s.adapter.ExecuteMigration(ctx, "PRAGMA foreign_keys = ON")
		}

	case "clickhouse", "scylla", "scylladb", "cassandra":
		// ClickHouse/ScyllaDB have no FK constraints
		return func() {}

	default: // postgresql, postgres
		var original string
		res, err := s.adapter.ExecuteQuery(ctx, "SHOW session_replication_role")
		if err == nil && len(res.Rows) > 0 {
			for _, v := range res.Rows[0] {
				original = fmt.Sprintf("%v", v)
				break
			}
		}
		if original == "replica" {
			// Already disabled
			return func() {}
		}
		if original == "" {
			original = "origin"
		}
		_ = s.adapter.ExecuteMigration(ctx, "SET session_replication_role = 'replica'")
		return func() {
			_ = s.adapter.ExecuteMigration(ctx, fmt.Sprintf("SET session_replication_role = '%s'", original))
		}
	}
}

// createEnumType creates a PostgreSQL ENUM type.
func (s *Service) createEnumType(ctx context.Context, enumType common.ExportEnumType) error {
	quotedValues := make([]string, len(enumType.Values))
	for i, v := range enumType.Values {
		quotedValues[i] = fmt.Sprintf("'%s'", strings.ReplaceAll(v, "'", "''"))
	}
	// Use unquoted name so PostgreSQL lowercases it, matching unquoted column type refs
	query := fmt.Sprintf("CREATE TYPE %s AS ENUM (%s)",
		enumType.Name,
		strings.Join(quotedValues, ", "))
	return s.adapter.ExecuteMigration(ctx, query)
}

// ImportDatabase imports data from an export file
func (s *Service) ImportDatabase(importData *common.ExportData) (*common.ImportResult, error) {
	_ = s.ensureCorrectSchema()

	result := &common.ImportResult{
		EnumTypesCreated: make([]string, 0),
		TablesCreated:    make([]string, 0),
		TablesUpdated:    make([]string, 0),
		Errors:           make([]string, 0),
	}

	ctx, cancel := context.WithTimeout(s.ctx, 300*time.Second)
	defer cancel()

	// Phase 0: Create ENUM types first (before tables)
	if len(importData.EnumTypes) > 0 {
		for _, enumType := range importData.EnumTypes {
			if err := s.createEnumType(ctx, enumType); err != nil {
				// Check if enum already exists (not an error)
				if !strings.Contains(err.Error(), "already exists") {
					result.Errors = append(result.Errors, fmt.Sprintf("Failed to create enum %s: %v", enumType.Name, err))
				}
			} else {
				result.EnumTypesCreated = append(result.EnumTypesCreated, enumType.Name)
			}
		}
	}

	// Get existing tables
	existingTables, err := s.adapter.GetAllTableNames(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get existing tables: %w", err)
	}
	existingTableMap := make(map[string]bool)
	for _, t := range existingTables {
		existingTableMap[t] = true
	}

	// Sort tables by dependency order
	sortedTables := s.sortImportTablesByDependency(importData.Tables)

	// Phase 1: Create tables (FKs included inline via REFERENCES)
	for _, table := range sortedTables {
		tableExists := existingTableMap[table.Name]

		if table.Schema != nil {
			if !tableExists {
				if err := s.createTableFromSchemaNoFK(ctx, table.Name, table.Schema); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("Failed to create table %s: %v", table.Name, err))
					continue
				}
				result.TablesCreated = append(result.TablesCreated, table.Name)
				existingTableMap[table.Name] = true
			} else {
				// Update existing table - add missing columns
				added, err := s.updateTableSchema(ctx, table.Name, table.Schema)
				if err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("Failed to update schema for %s: %v", table.Name, err))
				} else {
					result.ColumnsAdded += added
					if added > 0 {
						result.TablesUpdated = append(result.TablesUpdated, table.Name)
					}
				}
			}
		}
	}

	// Phase 2: Disable FK checks (if enabled) and import data in dependency order
	restoreFK := s.disableFKChecksIfNeeded(ctx)
	for _, table := range sortedTables {
		if len(table.Data) > 0 && existingTableMap[table.Name] {
			inserted, updated, err := s.importTableData(ctx, table.Name, table.Data)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("Failed to import data for %s: %v", table.Name, err))
			} else {
				result.RowsInserted += inserted
				result.RowsUpdated += updated
			}
		}
	}
	restoreFK()

	return result, nil
}

// normalizeColType converts Prisma/export column types to valid PostgreSQL types.
func normalizeColType(t string) string {
	if strings.HasPrefix(t, "_") {
		return strings.ToUpper(t[1:]) + "[]"
	}
	return t
}

// normalizeDefault fixes default values that need type casting after type normalization.
func normalizeDefault(def, colType string) string {
	if def == "" {
		return ""
	}
	// ARRAY[] default needs explicit cast for array types
	if def == "ARRAY[]" && strings.HasSuffix(colType, "[]") {
		return fmt.Sprintf("ARRAY[]::%s", colType)
	}
	return def
}

// createTableFromSchemaNoFK creates a new table from the export schema WITHOUT foreign key constraints
func (s *Service) createTableFromSchemaNoFK(ctx context.Context, tableName string, schema *common.ExportTableSchema) error {
	provider := ""
	if s.cfg != nil {
		provider = s.cfg.Database.Provider
	}

	// Use adapter-native DDL for ClickHouse (MergeTree, no SQL constraints)
	if provider == "clickhouse" {
		return s.createTableClickHouse(ctx, tableName, schema)
	}

	// Use CQL DDL for ScyllaDB
	if provider == "scylla" || provider == "scylladb" || provider == "cassandra" {
		return s.createTableScylla(ctx, tableName, schema)
	}

	var columnDefs []string
	var pkCols []string

	for _, col := range schema.Columns {
		if col.PrimaryKey {
			pkCols = append(pkCols, common.QuoteIdentifier(col.Name))
		}
	}
	compositePK := len(pkCols) > 1

	for _, col := range schema.Columns {
		colType := normalizeColType(col.Type)

		// GENERATED ALWAYS AS (computed columns)
		if col.Generated != "" {
			def := fmt.Sprintf("%s %s GENERATED ALWAYS AS (%s) STORED",
				common.QuoteIdentifier(col.Name), colType, col.Generated)
			columnDefs = append(columnDefs, def)
			continue
		}

		// GENERATED ALWAYS AS IDENTITY (PostgreSQL)
		if col.IsIdentity {
			def := fmt.Sprintf("%s %s GENERATED ALWAYS AS IDENTITY",
				common.QuoteIdentifier(col.Name), colType)
			if col.PrimaryKey && !compositePK {
				def += " PRIMARY KEY"
			}
			columnDefs = append(columnDefs, def)
			continue
		}

		def := fmt.Sprintf("%s %s", common.QuoteIdentifier(col.Name), colType)

		if col.PrimaryKey && !compositePK {
			def += " PRIMARY KEY"
		}
		if col.AutoIncrement {
			if provider == "mysql" {
				def += " AUTO_INCREMENT"
			}
		}
		if !col.Nullable && !col.PrimaryKey {
			def += " NOT NULL"
		}
		if col.Unique && !col.PrimaryKey {
			def += " UNIQUE"
		}
		if col.Default != "" {
			normalized := normalizeDefault(col.Default, colType)
			if normalized != "" {
				def += fmt.Sprintf(" DEFAULT %s", normalized)
			}
		}
		if col.Check != "" {
			def += fmt.Sprintf(" CHECK (%s)", col.Check)
		}

		// ON DELETE / ON UPDATE for self-referencing or dependent tables
		if col.ForeignKeyTable != "" && col.ForeignKeyColumn != "" {
			ref := fmt.Sprintf(" REFERENCES %s(%s)",
				common.QuoteIdentifier(col.ForeignKeyTable),
				common.QuoteIdentifier(col.ForeignKeyColumn))
			if col.OnDeleteAction != "" {
				ref += fmt.Sprintf(" ON DELETE %s", col.OnDeleteAction)
			}
			if col.OnUpdateAction != "" {
				ref += fmt.Sprintf(" ON UPDATE %s", col.OnUpdateAction)
			}
			def += ref
		}

		columnDefs = append(columnDefs, def)
	}

	if compositePK {
		columnDefs = append(columnDefs, fmt.Sprintf("PRIMARY KEY (%s)", strings.Join(pkCols, ", ")))
	}

	query := fmt.Sprintf("CREATE TABLE %s (\n  %s\n)",
		common.QuoteIdentifier(tableName),
		strings.Join(columnDefs, ",\n  "))

	if err := s.adapter.ExecuteMigration(ctx, query); err != nil {
		return err
	}

	// Create indexes defined on the table
	if len(schema.Indexes) > 0 {
		for _, idx := range schema.Indexes {
			if sql := s.buildCreateIndexSQL(tableName, idx); sql != "" {
				_ = s.adapter.ExecuteMigration(ctx, sql)
			}
		}
	}

	return nil
}

// createTableClickHouse creates a table using MergeTree engine for ClickHouse
func (s *Service) createTableClickHouse(ctx context.Context, tableName string, schema *common.ExportTableSchema) error {
	var columnDefs []string
	var pkCols []string

	for _, col := range schema.Columns {
		if col.PrimaryKey {
			pkCols = append(pkCols, common.QuoteIdentifier(col.Name))
		}
	}

	for _, col := range schema.Columns {
		colType := normalizeColType(col.Type)
		def := fmt.Sprintf("%s %s", common.QuoteIdentifier(col.Name), colType)
		if col.Default != "" {
			def += fmt.Sprintf(" DEFAULT %s", col.Default)
		}
		columnDefs = append(columnDefs, def)
	}

	orderCols := pkCols
	if len(orderCols) == 0 && len(schema.Columns) > 0 {
		orderCols = []string{common.QuoteIdentifier(schema.Columns[0].Name)}
	}
	if len(orderCols) == 0 {
		orderCols = []string{"tuple()"}
	}

	query := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n  %s\n) ENGINE = MergeTree() ORDER BY (%s)",
		common.QuoteIdentifier(tableName),
		strings.Join(columnDefs, ",\n  "),
		strings.Join(orderCols, ", "))

	if err := s.adapter.ExecuteMigration(ctx, query); err != nil {
		return err
	}

	// Add data-skipping indexes
	if len(schema.Indexes) > 0 {
		for _, idx := range schema.Indexes {
			idxType := "minmax"
			if idx.Unique {
				idxType = "bloom_filter"
			}
			cols := strings.Join(idx.Columns, ", ")
			idxSQL := fmt.Sprintf("ALTER TABLE %s ADD INDEX %s (%s) TYPE %s GRANULARITY 1",
				common.QuoteIdentifier(tableName),
				common.QuoteIdentifier(idx.Name), cols, idxType)
			_ = s.adapter.ExecuteMigration(ctx, idxSQL)
		}
	}

	return nil
}

// createTableScylla creates a table using CQL syntax for ScyllaDB
func (s *Service) createTableScylla(ctx context.Context, tableName string, schema *common.ExportTableSchema) error {
	var columnDefs []string
	var pkCols []string

	for _, col := range schema.Columns {
		if col.PrimaryKey {
			pkCols = append(pkCols, col.Name)
		}
	}

	for _, col := range schema.Columns {
		colType := normalizeColType(col.Type)
		def := fmt.Sprintf("%s %s", common.QuoteIdentifier(col.Name), colType)
		columnDefs = append(columnDefs, def)
	}

	query := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n  %s\n  PRIMARY KEY (%s)\n)",
		common.QuoteIdentifier(tableName),
		strings.Join(columnDefs, ",\n  "),
		func() string {
			if len(pkCols) > 0 {
				quoted := make([]string, len(pkCols))
				for i, c := range pkCols {
					quoted[i] = common.QuoteIdentifier(c)
				}
				return strings.Join(quoted, ", ")
			}
			if len(schema.Columns) > 0 {
				return common.QuoteIdentifier(schema.Columns[0].Name)
			}
			return "id"
		}())

	return s.adapter.ExecuteMigration(ctx, query)
}

// buildCreateIndexSQL generates CREATE INDEX DDL from export data
func (s *Service) buildCreateIndexSQL(tableName string, idx common.ExportIndex) string {
	if idx.Unique && idx.Name == "" {
		return ""
	}

	createClause := "CREATE"
	if idx.Unique {
		createClause = "CREATE UNIQUE"
	}

	nameClause := ""
	if idx.Name != "" {
		nameClause = fmt.Sprintf(" %s", common.QuoteIdentifier(idx.Name))
	}

	methodClause := ""
	if idx.Method != "" {
		methodClause = fmt.Sprintf(" USING %s", idx.Method)
	}

	var colExprs []string
	if len(idx.Expr) > 0 {
		for _, e := range idx.Expr {
			colExprs = append(colExprs, fmt.Sprintf("(%s)", e))
		}
	}
	for _, c := range idx.Columns {
		colExprs = append(colExprs, common.QuoteIdentifier(c))
	}

	query := fmt.Sprintf("%s INDEX%s ON %s%s (%s)",
		createClause, nameClause,
		common.QuoteIdentifier(tableName),
		methodClause,
		strings.Join(colExprs, ", "))

	if idx.Where != "" {
		query += fmt.Sprintf(" WHERE %s", idx.Where)
	}
	return query
}

// updateTableSchema updates an existing table by adding missing columns
func (s *Service) updateTableSchema(ctx context.Context, tableName string, schema *common.ExportTableSchema) (int, error) {
	// Get existing columns
	existingCols, err := s.adapter.GetTableColumns(ctx, tableName)
	if err != nil {
		return 0, err
	}

	existingColMap := make(map[string]bool)
	for _, col := range existingCols {
		existingColMap[col.Name] = true
	}

	added := 0
	for _, col := range schema.Columns {
		if existingColMap[col.Name] {
			continue // Column already exists
		}

		// Add the missing column
		def := col.Type
		if !col.Nullable {
			// For adding columns, we need to allow NULL or provide a default
			if col.Default != "" {
				def += fmt.Sprintf(" DEFAULT %s", col.Default)
			}
		}
		if col.Unique {
			def += " UNIQUE"
		}
		if col.Default != "" && col.Nullable {
			def += fmt.Sprintf(" DEFAULT %s", col.Default)
		}

		query := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s",
			common.QuoteIdentifier(tableName),
			common.QuoteIdentifier(col.Name),
			def)

		if err := s.adapter.ExecuteMigration(ctx, query); err != nil {
			return added, fmt.Errorf("failed to add column %s: %w", col.Name, err)
		}
		added++
	}

	return added, nil
}

// sqlLiteral converts a Go value to a SQL literal string.
func sqlLiteral(v any) string {
	if v == nil {
		return "NULL"
	}
	if arr, ok := v.([]any); ok {
		elems := make([]string, len(arr))
		for i, elem := range arr {
			if elem == nil {
				elems[i] = "NULL"
			} else {
				s := strings.ReplaceAll(fmt.Sprintf("%v", elem), `"`, `\"`)
				elems[i] = `"` + s + `"`
			}
		}
		return fmt.Sprintf("'{%s}'", strings.Join(elems, ","))
	}
	// JSON numbers decode as float64 — emit without decimal if whole number
	if f, ok := v.(float64); ok {
		if f == float64(int64(f)) {
			return fmt.Sprintf("'%d'", int64(f))
		}
		return fmt.Sprintf("'%g'", f)
	}
	strVal := fmt.Sprintf("%v", v)
	escaped := strings.ReplaceAll(strVal, "'", "''")
	return fmt.Sprintf("'%s'", escaped)
}
func (s *Service) importTableData(ctx context.Context, tableName string, data []map[string]any) (int, int, error) {
	if len(data) == 0 {
		return 0, 0, nil
	}

	// Collect stable column order from first row
	var colNames []string
	for col := range data[0] {
		colNames = append(colNames, col)
	}
	var quotedCols []string
	for _, col := range colNames {
		quotedCols = append(quotedCols, common.QuoteIdentifier(col))
	}
	colList := strings.Join(quotedCols, ", ")

	inserted := 0
	const batchSize = 500
	for i := 0; i < len(data); i += batchSize {
		end := min(i+batchSize, len(data))
		batch := data[i:end]

		var valueGroups []string
		for _, row := range batch {
			var vals []string
			for _, col := range colNames {
				vals = append(vals, sqlLiteral(row[col]))
			}
			valueGroups = append(valueGroups, "("+strings.Join(vals, ", ")+")")
		}

		query := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s ON CONFLICT DO NOTHING",
			common.QuoteIdentifier(tableName), colList,
			strings.Join(valueGroups, ", "))

		if err := s.adapter.ExecuteMigration(ctx, query); err != nil {
			// Fallback: single-row inserts to skip individual bad rows
			for _, row := range batch {
				var vals []string
				for _, col := range colNames {
					vals = append(vals, sqlLiteral(row[col]))
				}
				single := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT DO NOTHING",
					common.QuoteIdentifier(tableName), colList,
					strings.Join(vals, ", "))
				if err2 := s.adapter.ExecuteMigration(ctx, single); err2 == nil {
					inserted++
				}
			}
		} else {
			inserted += len(batch)
		}
	}

	return inserted, 0, nil
}
