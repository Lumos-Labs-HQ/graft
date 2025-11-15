package sqlite

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

func (a *Adapter) CreateBranchSchema(ctx context.Context, branchName string) error {
	// For SQLite, this just validates the branch name
	// Actual file creation happens in CloneSchemaToBranch
	return nil
}

func (a *Adapter) DropBranchSchema(ctx context.Context, branchName string) error {
	// Delete the branch database file
	branchFile := a.getBranchFilePath(branchName)
	if err := os.Remove(branchFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete branch file: %w", err)
	}
	return nil
}

func (a *Adapter) CloneSchemaToBranch(ctx context.Context, sourceSchema, targetSchema string) error {
	// For SQLite, sourceSchema is the branch name, targetSchema is the new branch name
	var sourceFile string
	if sourceSchema == "" || sourceSchema == "main" {
		sourceFile = a.originalPath
	} else {
		sourceFile = a.getBranchFilePath(sourceSchema)
	}
	
	targetFile := a.getBranchFilePath(targetSchema)
	
	// Copy the database file
	if err := copyFile(sourceFile, targetFile); err != nil {
		return fmt.Errorf("failed to clone database file: %w", err)
	}
	
	return nil
}

func (a *Adapter) GetSchemaForBranch(ctx context.Context, branchSchema string) ([]types.SchemaTable, error) {
	// Switch to branch file temporarily
	originalPath := a.currentPath
	defer a.SwitchDatabase(ctx, originalPath)
	
	branchFile := a.getBranchFilePath(branchSchema)
	if err := a.SwitchDatabase(ctx, branchFile); err != nil {
		return nil, err
	}
	
	return a.GetCurrentSchema(ctx)
}

func (a *Adapter) SetActiveSchema(ctx context.Context, schemaName string) error {
	// For SQLite, switch to the branch file
	var branchFile string
	if schemaName == "" || schemaName == "main" {
		branchFile = a.originalPath
	} else {
		branchFile = a.getBranchFilePath(schemaName)
	}
	return a.SwitchDatabase(ctx, branchFile)
}

func (a *Adapter) GetTableNamesInSchema(ctx context.Context, schemaName string) ([]string, error) {
	// Switch to branch file temporarily
	originalPath := a.currentPath
	defer a.SwitchDatabase(ctx, originalPath)
	
	branchFile := a.getBranchFilePath(schemaName)
	if err := a.SwitchDatabase(ctx, branchFile); err != nil {
		return nil, err
	}
	
	return a.GetAllTableNames(ctx)
}

func (a *Adapter) getBranchFilePath(branchName string) string {
	// Get directory and base name from original path
	dir := filepath.Dir(a.originalPath)
	ext := filepath.Ext(a.originalPath)
	base := strings.TrimSuffix(filepath.Base(a.originalPath), ext)
	
	// Create branch file name: database_branch_branchname.db
	return filepath.Join(dir, fmt.Sprintf("%s_branch_%s%s", base, branchName, ext))
}

func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	if _, err := io.Copy(destFile, sourceFile); err != nil {
		return err
	}

	return destFile.Sync()
}
