package export

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Lumos-Labs-HQ/flash/internal/database"
	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

func PerformExport(ctx context.Context, adapter database.DatabaseAdapter, exportPath string) (string, error) {
	tables, err := adapter.GetAllTableNames(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get table names: %w", err)
	}

	if len(tables) == 0 {
		log.Println("No tables found in database")
		return "", nil
	}

	// Fetch full schema for v2 schema-inclusive export
	schemaTables, err := adapter.PullCompleteSchema(ctx)
	if err != nil {
		log.Printf("Warning: could not pull schema for export: %v", err)
		schemaTables = nil
	}
	schemaEnums, err := adapter.GetCurrentEnums(ctx)
	if err != nil {
		schemaEnums = nil
	}

	indexMap := make(map[string][]types.SchemaIndex)
	for _, t := range schemaTables {
		indexMap[t.Name] = t.Indexes
	}

	exportData := types.BackupData{
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
		Version:   "1.0",
		Tables:    make(map[string]any, len(tables)),
		Comment:   "Database export",
	}

	// Fetch table data in parallel
	type tableResult struct {
		name string
		data []map[string]any
		err  error
	}

	var validTables []string
	for _, tableName := range tables {
		if tableName != "_flash_migrations" {
			validTables = append(validTables, tableName)
		}
	}

	results := make(chan tableResult, len(validTables))
	var wg sync.WaitGroup

	for _, tableName := range validTables {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			data, err := adapter.GetTableData(ctx, name)
			results <- tableResult{name, data, err}
		}(tableName)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	for result := range results {
		if result.err != nil {
			log.Printf("Warning: Failed to get data for table %s: %v", result.name, result.err)
		} else {
			exportData.Tables[result.name] = result.data
		}
	}

	return exportToJSON(exportData, exportPath, schemaTables, schemaEnums, indexMap)
}

func exportToJSON(data types.BackupData, exportPath string, schemaTables []types.SchemaTable, schemaEnums []types.SchemaEnum, indexMap map[string][]types.SchemaIndex) (string, error) {
	if err := os.MkdirAll(exportPath, 0755); err != nil {
		return "", fmt.Errorf("failed to create export directory: %w", err)
	}

	timestamp := time.Now().Format("2006-01-02_15-04-05")
	filePath := filepath.Join(exportPath, fmt.Sprintf("export_%s.json", timestamp))

	payload := map[string]any{
		"version":   "2.0",
		"timestamp": data.Timestamp,
		"comment":   data.Comment,
		"data":      data.Tables,
	}

	if schemaTables != nil || schemaEnums != nil {
		schema := buildSchemaDDL(schemaTables, schemaEnums, indexMap)
		payload["schema"] = schema
	}

	jsonData, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal data: %w", err)
	}

	if err := os.WriteFile(filePath, jsonData, 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	return filePath, nil
}

func buildSchemaDDL(tables []types.SchemaTable, enums []types.SchemaEnum, indexMap map[string][]types.SchemaIndex) map[string]any {
	schema := map[string]any{}

	if len(enums) > 0 {
		enumList := make([]map[string]any, len(enums))
		for i, e := range enums {
			enumList[i] = map[string]any{
				"name":   e.Name,
				"values": e.Values,
			}
		}
		schema["enums"] = enumList
	}

	if tables != nil {
		tableList := make([]map[string]any, 0, len(tables))
		for _, t := range tables {
			if strings.HasPrefix(t.Name, "_flash_") {
				continue
			}
			cols := make([]map[string]any, len(t.Columns))
			for j, c := range t.Columns {
				col := map[string]any{
					"name":       c.Name,
					"type":       c.Type,
					"nullable":   c.Nullable,
					"default":    c.Default,
					"is_primary": c.IsPrimary,
					"is_unique":  c.IsUnique,
					"check":      c.Check,
					"generated":  c.Generated,
				}
				// Only include FK fields if they have values (not noise)
				if c.ForeignKeyTable != "" {
					col["foreign_key_table"] = c.ForeignKeyTable
					col["foreign_key_column"] = c.ForeignKeyColumn
					if c.OnDeleteAction != "" && c.OnDeleteAction != "NO ACTION" {
						col["on_delete"] = c.OnDeleteAction
					}
				}
				cols[j] = col
			}
			idxList := indexMap[t.Name]
			tableList = append(tableList, map[string]any{
				"name":    t.Name,
				"columns": cols,
				"indexes": idxList,
			})
		}
		schema["tables"] = tableList
	}

	return schema
}
