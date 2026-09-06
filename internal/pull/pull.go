package pull

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/config"
	"github.com/Lumos-Labs-HQ/flash/internal/database"
	"github.com/Lumos-Labs-HQ/flash/internal/schema"
	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

type Options struct {
	Backup     bool
	OutputPath string
}

type Service struct {
	config     *config.Config
	adapter    database.DatabaseAdapter
	comparator *schema.SQLComparator
}

func NewService(cfg *config.Config) (*Service, error) {
	adapter := database.NewAdapter(cfg.Database.Provider)

	dbURL, err := cfg.GetDatabaseURL()
	if err != nil {
		return nil, fmt.Errorf("failed to get database URL: %w", err)
	}

	if err := adapter.Connect(context.Background(), dbURL); err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return &Service{
		config:     cfg,
		adapter:    adapter,
		comparator: schema.NewSQLComparator(),
	}, nil
}

func (s *Service) Close() {
	if s.adapter != nil {
		s.adapter.Close()
	}
}

// PullSchema intelligently pulls database schema
// - If no schema files exist: creates single schema.sql with everything
// - If schema files exist: compares and updates only changed parts in-place
// - New tables: creates new .sql file for that table
// - Changed columns: updates only those lines in existing files
func (s *Service) PullSchema(ctx context.Context, opts Options) error {
	fmt.Println("🔍 Introspecting database schema...")

	dbTables, err := s.adapter.PullCompleteSchema(ctx)
	if err != nil {
		return fmt.Errorf("failed to pull database schema: %w", err)
	}

	dbEnums, err := s.adapter.GetCurrentEnums(ctx)
	if err != nil {
		dbEnums = []types.SchemaEnum{}
	}

	if len(dbTables) == 0 && len(dbEnums) == 0 {
		fmt.Println("📄 No tables or enums found in database")
		return nil
	}

	// Get schema directory
	schemaDir := s.config.GetSchemaDir()
	if opts.OutputPath != "" {
		schemaDir = opts.OutputPath
	}

	// Check if any schema files exist
	existingFiles, err := s.getExistingSchemaFiles(schemaDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to check existing schema files: %w", err)
	}

	// Create backup if requested
	if opts.Backup && len(existingFiles) > 0 {
		if err := s.createDirBackup(schemaDir); err != nil {
			fmt.Printf("⚠️  Warning: Failed to create backup: %v\n", err)
		} else {
			fmt.Println("💾 Created backup of existing schema files")
		}
	}

	// Ensure schema directory exists
	if err := os.MkdirAll(schemaDir, 0755); err != nil {
		return fmt.Errorf("failed to create schema directory: %w", err)
	}

	// Get indexes from database
	dbIndexes, err := s.getTableIndexes(ctx, dbTables)
	if err != nil {
		fmt.Printf("⚠️  Warning: Could not fetch indexes: %v\n", err)
		dbIndexes = make(map[string][]types.SchemaIndex)
	}

	// If no files exist, create single schema.sql
	if len(existingFiles) == 0 {
		return s.createSingleSchemaFile(schemaDir, dbTables, dbEnums, dbIndexes)
	}

	existingTables, existingEnums := s.parseExistingSchemaFiles(existingFiles)

	return s.smartUpdateSchema(schemaDir, existingFiles, existingTables, existingEnums, dbTables, dbEnums, dbIndexes)
}

// getExistingSchemaFiles returns all .sql files in the schema directory
func (s *Service) getExistingSchemaFiles(schemaDir string) (map[string]string, error) {
	entries, err := os.ReadDir(schemaDir)
	if err != nil {
		return nil, err
	}

	files := make(map[string]string)
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			filePath := filepath.Join(schemaDir, entry.Name())
			content, err := os.ReadFile(filePath)
			if err != nil {
				continue
			}
			files[entry.Name()] = string(content)
		}
	}
	return files, nil
}

var (
	pullCreateTableRegex = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?["'\x60]?(\w+)["'\x60]?`)
	pullCreateEnumRegex  = regexp.MustCompile(`(?i)CREATE\s+TYPE\s+["'\x60]?(\w+)["'\x60]?\s+AS\s+ENUM`)
)

// parseExistingSchemaFiles extracts table names from existing schema files,
// ignoring lines that are commented out.
func (s *Service) parseExistingSchemaFiles(files map[string]string) (map[string]string, []string) {
	tables := make(map[string]string)
	var enums []string

	for fileName, content := range files {
		for line := range strings.SplitSeq(content, "\n") {
			trimmed := strings.TrimSpace(line)
			stripped := strings.TrimSpace(strings.TrimPrefix(trimmed, "--"))
			if m := pullCreateTableRegex.FindStringSubmatch(stripped); len(m) > 1 {
				tables[m[1]] = fileName
			}
			if m := pullCreateEnumRegex.FindStringSubmatch(stripped); len(m) > 1 {
				enums = append(enums, m[1])
			}
		}
	}

	return tables, enums
}

// createSingleSchemaFile creates a single schema.sql with all tables
func (s *Service) createSingleSchemaFile(schemaDir string, dbTables []types.SchemaTable, dbEnums []types.SchemaEnum, dbIndexes map[string][]types.SchemaIndex) error {
	var sb strings.Builder

	if len(dbEnums) > 0 {
		sb.WriteString(s.generateEnumSQL(dbEnums))
		sb.WriteString("\n\n")
	}

	sort.Slice(dbTables, func(i, j int) bool {
		return dbTables[i].Name < dbTables[j].Name
	})

	// Write all tables
	for _, table := range dbTables {
		if strings.HasPrefix(table.Name, "_flash_") {
			continue
		}
		sb.WriteString(s.generateTableSQL(table, dbIndexes[table.Name]))
		sb.WriteString("\n")
	}

	schemaPath := filepath.Join(schemaDir, "schema.sql")
	if err := os.WriteFile(schemaPath, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("failed to write schema file: %w", err)
	}

	fmt.Printf("✅ Created %s with %d tables", schemaPath, len(dbTables))
	if len(dbEnums) > 0 {
		fmt.Printf(" and %d enums", len(dbEnums))
	}
	fmt.Println()

	return nil
}

// smartUpdateSchema compares and updates only changed parts
func (s *Service) smartUpdateSchema(schemaDir string, existingFiles map[string]string, existingTables map[string]string, existingEnums []string, dbTables []types.SchemaTable, dbEnums []types.SchemaEnum, dbIndexes map[string][]types.SchemaIndex) error {
	updatedFiles := 0
	newFiles := 0
	commentedFiles := 0

	// Create a map of db tables for quick lookup
	dbTableMap := make(map[string]types.SchemaTable)
	for _, table := range dbTables {
		if !strings.HasPrefix(table.Name, "_flash_") {
			dbTableMap[table.Name] = table
		}
	}

	// Build reverse map: fileName -> []tableName for O(1) file lookup
	fileToTables := make(map[string][]string)
	for tableName, fileName := range existingTables {
		fileToTables[fileName] = append(fileToTables[fileName], tableName)
	}

	// Track which files need updating
	fileUpdates := make(map[string]string)

	// Track tables that exist in files but NOT in the database (dropped tables)
	droppedTables := make(map[string]string) // tableName -> fileName

	// Process each existing file — O(files + tables) instead of O(files × tables)
	for fileName, content := range existingFiles {
		updatedContent := content
		fileChanged := false

		// Find all tables in this file and update them
		for _, tableName := range fileToTables[fileName] {
			if dbTable, exists := dbTableMap[tableName]; exists {
				newTableSQL := s.generateTableSQLClean(dbTable, dbIndexes[tableName])

				// If file is fully commented (no active SQL), replace the whole file
				if s.isFileCommentedOut(content) {
					fileUpdates[fileName] = newTableSQL + "\n"
					fileChanged = true
					delete(dbTableMap, tableName)
					fmt.Printf("  📝 Restored table %s in %s\n", tableName, fileName)
					break // file replaced, stop processing this file
				}

				existingTableSQL := s.extractTableSQL(content, tableName)
				if existingTableSQL != "" && !s.compareTableSQL(existingTableSQL, newTableSQL) {
					updatedContent = s.replaceTableInContent(updatedContent, tableName, newTableSQL)
					fileChanged = true
					fmt.Printf("  📝 Updated table %s in %s\n", tableName, fileName)
				}

				delete(dbTableMap, tableName)
			} else {
				droppedTables[tableName] = fileName
			}
		}

		if fileChanged {
			// Only set from updatedContent if not already replaced wholesale
			if _, alreadySet := fileUpdates[fileName]; !alreadySet {
				fileUpdates[fileName] = updatedContent
			}
			updatedFiles++
		}
	}

	// Comment out files for dropped tables
	for tableName, fileName := range droppedTables {
		content := existingFiles[fileName]

		// Check if the file is already commented out
		if s.isFileCommentedOut(content) {
			continue
		}

		// Comment out the entire file content
		commentedContent := s.commentOutFile(content, tableName)

		filePath := filepath.Join(schemaDir, fileName)
		if err := os.WriteFile(filePath, []byte(commentedContent), 0644); err != nil {
			return fmt.Errorf("failed to comment out file %s: %w", fileName, err)
		}

		fmt.Printf("  🗑️  Commented out %s (table '%s' no longer exists in database)\n", fileName, tableName)
		commentedFiles++
	}

	// Write updated files
	for fileName, content := range fileUpdates {
		filePath := filepath.Join(schemaDir, fileName)
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			return fmt.Errorf("failed to write updated file %s: %w", fileName, err)
		}
	}

	// Create new files for remaining (new) tables
	for tableName, table := range dbTableMap {
		tableSQL := s.generateTableSQL(table, dbIndexes[tableName])
		fileName := fmt.Sprintf("%s.sql", tableName)
		filePath := filepath.Join(schemaDir, fileName)

		if err := os.WriteFile(filePath, []byte(tableSQL), 0644); err != nil {
			return fmt.Errorf("failed to write new table file %s: %w", fileName, err)
		}

		fmt.Printf("  ✨ Created new file %s for table %s\n", fileName, tableName)
		newFiles++
	}

	// Handle enums - update or create _enums.sql if needed
	if len(dbEnums) > 0 {
		enumSQL := s.generateEnumSQL(dbEnums)
		enumPath := filepath.Join(schemaDir, "_enums.sql")

		existingEnumContent, _ := os.ReadFile(enumPath)
		if string(existingEnumContent) != enumSQL {
			if err := os.WriteFile(enumPath, []byte(enumSQL), 0644); err != nil {
				return fmt.Errorf("failed to write enum file: %w", err)
			}
			fmt.Printf("  📝 Updated _enums.sql (%d enums)\n", len(dbEnums))
			updatedFiles++
		}
	}

	if updatedFiles > 0 || newFiles > 0 || commentedFiles > 0 {
		fmt.Println("✅ Schema synced")
	} else {
		fmt.Println("✅ Schema is up to date")
	}

	return nil
}
