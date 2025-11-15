package studio

import (
	"context"
	"fmt"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/branch"
	"github.com/Lumos-Labs-HQ/flash/internal/config"
	"github.com/Lumos-Labs-HQ/flash/internal/database"
)

type Service struct {
	adapter database.DatabaseAdapter
	ctx     context.Context
}

func NewService(adapter database.DatabaseAdapter) *Service {
	return &Service{
		adapter: adapter,
		ctx:     context.Background(),
	}
}


func (s *Service) ensureCorrectSchema() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	
	branchMgr := branch.NewMetadataManager(cfg.MigrationsPath)
	store, err := branchMgr.Load()
	if err != nil {
		return err
	}
	currentBranch := store.GetBranch(store.Current)
	if currentBranch == nil {
		return nil
	}
	
	if cfg.Database.Provider == "postgresql" || cfg.Database.Provider == "postgres" {
		query := fmt.Sprintf("SET search_path TO %s, public", currentBranch.Schema)
		_, err = s.adapter.ExecuteQuery(s.ctx, query)
		return err
	} else if cfg.Database.Provider == "mysql" || cfg.Database.Provider == "sqlite" || cfg.Database.Provider == "sqlite3" {
		type DatabaseSwitcher interface {
			SwitchDatabase(ctx context.Context, dbName string) error
		}
		if switcher, ok := s.adapter.(DatabaseSwitcher); ok {
			return switcher.SwitchDatabase(s.ctx, currentBranch.Schema)
		}
	}
	
	return nil
}

func (s *Service) GetTables() ([]TableInfo, error) {
	s.ensureCorrectSchema()
	tables, err := s.adapter.GetAllTableNames(s.ctx)
	if err != nil {
		return nil, err
	}

	// Pre-allocate with estimated capacity
	result := make([]TableInfo, 0, len(tables))

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
		result = append(result, TableInfo{
			Name:     table,
			RowCount: tableCounts[table],
		})
	}

	return result, nil
}

func (s *Service) GetTableData(tableName string, page, limit int) (*TableData, error) {
	s.ensureCorrectSchema()
	schema, err := s.adapter.GetTableColumns(s.ctx, tableName)
	if err != nil {
		return nil, err
	}

	columns := make([]ColumnInfo, len(schema))
	for i, col := range schema {
		columns[i] = ColumnInfo{
			Name:             col.Name,
			Type:             col.Type,
			Nullable:         col.Nullable,
			PrimaryKey:       col.IsPrimary,
			Default:          col.Default,
			AutoIncrement:    col.IsAutoIncrement,
			ForeignKeyTable:  col.ForeignKeyTable,
			ForeignKeyColumn: col.ForeignKeyColumn,
		}
	}

	offset := (page - 1) * limit
	rows, err := s.getRows(tableName, limit, offset)
	if err != nil {
		return nil, err
	}

	total, _ := s.getRowCount(tableName)

	return &TableData{
		Columns: columns,
		Rows:    rows,
		Total:   total,
		Page:    page,
		Limit:   limit,
	}, nil
}

func quoteIdentifier(name string) string {
	return fmt.Sprintf("\"%s\"", name)
}

func (s *Service) SaveChanges(tableName string, changes []RowChange) error {
	s.ensureCorrectSchema()
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

	for _, change := range changes {
		if change.Action == "update" {
			query := fmt.Sprintf("UPDATE %s SET %s = '%s' WHERE %s = '%s'",
				quoteIdentifier(tableName), quoteIdentifier(change.Column), change.Value, quoteIdentifier(pkColumn), change.RowID)

			if err := s.adapter.ExecuteMigration(s.ctx, query); err != nil {
				return fmt.Errorf("failed to update %s.%s: %w", tableName, change.Column, err)
			}
		}
	}

	return nil
}

func (s *Service) DeleteRows(tableName string, rowIDs []string) error {
	s.ensureCorrectSchema()
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
		query := fmt.Sprintf("DELETE FROM %s WHERE %s = '%s'", quoteIdentifier(tableName), quoteIdentifier(pkColumn), rowID)
		if err := s.adapter.ExecuteMigration(s.ctx, query); err != nil {
			return fmt.Errorf("failed to delete row %s: %w", rowID, err)
		}
	}

	return nil
}

func (s *Service) AddRow(tableName string, data map[string]any) error {
	s.ensureCorrectSchema()
	if len(data) == 0 {
		return fmt.Errorf("no data provided")
	}

	columns := []string{}
	values := []string{}

	for col, val := range data {
		columns = append(columns, quoteIdentifier(col))
		if val == nil {
			values = append(values, "NULL")
		} else {
			strVal := fmt.Sprintf("%v", val)
			escapedVal := strings.ReplaceAll(strVal, "'", "''")
			values = append(values, fmt.Sprintf("'%s'", escapedVal))
		}
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		quoteIdentifier(tableName),
		strings.Join(columns, ", "),
		strings.Join(values, ", "))

	return s.adapter.ExecuteMigration(s.ctx, query)
}

func (s *Service) DeleteRow(tableName, rowID string) error {
	return s.deleteRow(tableName, rowID)
}

// Helper methods
func (s *Service) getRowCount(tableName string) (int, error) {
	data, err := s.adapter.GetTableData(s.ctx, tableName)
	if err != nil {
		return 0, err
	}
	return len(data), nil
}

func (s *Service) getRows(tableName string, limit, offset int) ([]map[string]any, error) {
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

func (s *Service) deleteRow(tableName, rowID string) error {
	schema, err := s.adapter.GetTableColumns(s.ctx, tableName)
	if err != nil {
		escaped := strings.ReplaceAll(rowID, "'", "''")
		query := fmt.Sprintf("DELETE FROM %s WHERE id = '%s'", quoteIdentifier(tableName), escaped)
		return s.adapter.ExecuteMigration(s.ctx, query)
	}

	pkColumn := "id"
	for _, col := range schema {
		if col.IsPrimary {
			pkColumn = col.Name
			break
		}
	}

	escaped := strings.ReplaceAll(rowID, "'", "''")
	query := fmt.Sprintf("DELETE FROM %s WHERE %s = '%s'", quoteIdentifier(tableName), quoteIdentifier(pkColumn), escaped)
	return s.adapter.ExecuteMigration(s.ctx, query)
}

func joinColumns(cols []string) string {
	return strings.Join(cols, ", ")
}

// GetSchemaVisualization returns schema for visualization
func (s *Service) GetSchemaVisualization() (map[string]any, error) {
	s.ensureCorrectSchema()
	tables, err := s.adapter.GetCurrentSchema(s.ctx)
	if err != nil {
		return nil, err
	}

	enums, _ := s.adapter.GetCurrentEnums(s.ctx)

	nodes := make([]map[string]any, 0, len(tables))   // Pre-allocate
	nodeIndex := make(map[string]string, len(tables)) // Pre-allocate

	for i, table := range tables {
		nodeID := fmt.Sprintf("table-%d", i)
		nodeIndex[table.Name] = nodeID

		columns := make([]map[string]any, 0, len(table.Columns)) // Pre-allocate
		columnMap := make(map[string]bool, len(table.Columns))   // Pre-allocate

		for _, col := range table.Columns {
			// Only add column if not already present
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
				"label":   table.Name,
				"columns": columns,
			},
			"position": map[string]int{
				"x": 100 + (i%4)*300,
				"y": 100 + (i/4)*250,
			},
		})
	}

	estimatedEdges := len(tables) * 2
	edges := make([]map[string]any, 0, estimatedEdges)
	edgeMap := make(map[string]bool, estimatedEdges) // Pre-allocate

	for _, table := range tables {
		sourceID := nodeIndex[table.Name]
		for _, col := range table.Columns {
			if col.ForeignKeyTable != "" {
				if targetID, ok := nodeIndex[col.ForeignKeyTable]; ok {
					// Create unique edge ID to prevent duplicates
					edgeID := fmt.Sprintf("%s-%s-%s", sourceID, targetID, col.Name)

					if !edgeMap[edgeID] {
						edgeMap[edgeID] = true

						var targetColumn string
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

	return map[string]any{
		"nodes": nodes,
		"edges": edges,
		"enums": enums,
	}, nil
}

// ExecuteSQL executes a raw SQL query
func (s *Service) ExecuteSQL(query string) (*TableData, error) {
	s.ensureCorrectSchema()
	query = strings.TrimSpace(query)

	queryUpper := strings.ToUpper(query)
	isSelectQuery := strings.HasPrefix(queryUpper, "SELECT") ||
		strings.HasPrefix(queryUpper, "SHOW") ||
		strings.HasPrefix(queryUpper, "DESCRIBE") ||
		strings.HasPrefix(queryUpper, "EXPLAIN") ||
		strings.HasPrefix(queryUpper, "WITH")

	if isSelectQuery {
		result, err := s.adapter.ExecuteQuery(s.ctx, query)
		if err != nil {
			return nil, fmt.Errorf("query execution failed: %w", err)
		}

		columns := make([]ColumnInfo, len(result.Columns))
		for i, col := range result.Columns {
			columns[i] = ColumnInfo{
				Name: col,
				Type: "TEXT",
			}
		}

		return &TableData{
			Columns: columns,
			Rows:    result.Rows,
			Total:   len(result.Rows),
			Page:    1,
			Limit:   len(result.Rows),
		}, nil
	} else {
		if err := s.adapter.ExecuteMigration(s.ctx, query); err != nil {
			return nil, fmt.Errorf("query execution failed: %w", err)
		}

		// Return success message
		return &TableData{
			Columns: []ColumnInfo{},
			Rows:    []map[string]any{},
			Total:   0,
			Page:    1,
			Limit:   0,
		}, nil
	}
}

// UpdateRow updates a single row in a table
func (s *Service) UpdateRow(table string, id interface{}, data map[string]interface{}) error {
	var setClauses []string
	var values []interface{}

	i := 1
	for col, val := range data {
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", quoteIdentifier(col), i))
		values = append(values, val)
		i++
	}

	sql := fmt.Sprintf("UPDATE %s SET %s WHERE id = $%d",
		quoteIdentifier(table), strings.Join(setClauses, ", "), i)

	_, err := s.adapter.ExecuteQuery(s.ctx, sql)
	return err
}

// InsertRow inserts a new row into a table
func (s *Service) InsertRow(table string, data map[string]interface{}) error {
	var columns []string
	var placeholders []string
	var values []interface{}

	i := 1
	for col, val := range data {
		columns = append(columns, quoteIdentifier(col))
		placeholders = append(placeholders, fmt.Sprintf("$%d", i))
		values = append(values, val)
		i++
	}

	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		quoteIdentifier(table), strings.Join(columns, ", "), strings.Join(placeholders, ", "))

	_, err := s.adapter.ExecuteQuery(s.ctx, sql)
	return err
}

func (s *Service) GetBranches() ([]map[string]interface{}, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, "", err
	}

	manager, err := branch.NewManager(cfg)
	if err != nil {
		return nil, "", err
	}
	defer manager.Close()

	branches, current, err := manager.ListBranches()
	if err != nil {
		return nil, "", err
	}

	result := make([]map[string]interface{}, len(branches))
	for i, b := range branches {
		result[i] = map[string]interface{}{
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
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	manager, err := branch.NewManager(cfg)
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

	if cfg.Database.Provider == "postgresql" || cfg.Database.Provider == "postgres" {
		query := fmt.Sprintf("SET search_path TO %s, public", branchSchema)
		if _, err := s.adapter.ExecuteQuery(ctx, query); err != nil {
			return fmt.Errorf("failed to set search_path: %w", err)
		}
	} else if cfg.Database.Provider == "mysql" || cfg.Database.Provider == "sqlite" || cfg.Database.Provider == "sqlite3" {
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
