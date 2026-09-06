package pull

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

func (s *Service) createDirBackup(schemaDir string) error {
	entries, err := os.ReadDir(schemaDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	backupDir := schemaDir + ".backup"
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		srcPath := filepath.Join(schemaDir, entry.Name())
		dstPath := filepath.Join(backupDir, entry.Name())

		content, err := os.ReadFile(srcPath)
		if err != nil {
			continue
		}
		if err := os.WriteFile(dstPath, content, 0644); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) getTableIndexes(ctx context.Context, tables []types.SchemaTable) (map[string][]types.SchemaIndex, error) {
	result := make(map[string][]types.SchemaIndex, len(tables))

	// First, use indexes already attached to tables from PullCompleteSchema (zero extra queries)
	for _, table := range tables {
		if len(table.Indexes) > 0 {
			result[table.Name] = table.Indexes
		}
	}

	// For adapters that support batch index fetching, fill in any missing tables
	type BatchIndexFetcher interface {
		GetAllTablesIndexes(ctx context.Context, tableNames []string) (map[string][]types.SchemaIndex, error)
	}
	if fetcher, ok := s.adapter.(BatchIndexFetcher); ok {
		// Only query tables that didn't already have indexes from PullCompleteSchema
		var missing []string
		for _, table := range tables {
			if _, has := result[table.Name]; !has {
				missing = append(missing, table.Name)
			}
		}
		if len(missing) > 0 {
			batchResult, err := fetcher.GetAllTablesIndexes(ctx, missing)
			if err == nil {
				maps.Copy(result, batchResult)
			}
		}
	}

	return result, nil
}
