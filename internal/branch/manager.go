package branch

import (
	"context"
	"fmt"
	"time"

	"github.com/Lumos-Labs-HQ/flash/internal/config"
	"github.com/Lumos-Labs-HQ/flash/internal/database"
)

type Manager struct {
	adapter  database.DatabaseAdapter
	metadata *MetadataManager
	cfg      *config.Config
	provider string
}

func NewManager(cfg *config.Config) (*Manager, error) {
	adapter := database.NewAdapter(cfg.Database.Provider)
	
	dbURL, err := cfg.GetDatabaseURL()
	if err != nil {
		return nil, fmt.Errorf("failed to get database URL: %w", err)
	}

	if err := adapter.Connect(context.Background(), dbURL); err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return &Manager{
		adapter:  adapter,
		metadata: NewMetadataManager(cfg.MigrationsPath),
		cfg:      cfg,
		provider: cfg.Database.Provider,
	}, nil
}

func (m *Manager) CreateBranch(ctx context.Context, branchName string) error {
	store, err := m.metadata.Load()
	if err != nil {
		return err
	}

	if store.GetBranch(branchName) != nil {
		return fmt.Errorf("branch '%s' already exists", branchName)
	}

	currentBranch := store.GetBranch(store.Current)
	if currentBranch == nil {
		return fmt.Errorf("current branch '%s' not found", store.Current)
	}

	schemaName := m.generateSchemaName(branchName)

	if err := m.adapter.CloneSchemaToBranch(ctx, currentBranch.Schema, schemaName); err != nil {
		return fmt.Errorf("failed to clone schema: %w", err)
	}

	newBranch := &BranchMetadata{
		Name:      branchName,
		Parent:    store.Current,
		Schema:    schemaName,
		CreatedAt: time.Now(),
		IsDefault: false,
	}

	if err := store.AddBranch(newBranch); err != nil {
		return err
	}

	return m.metadata.Save(store)
}

func (m *Manager) SwitchBranch(ctx context.Context, branchName string) error {
	store, err := m.metadata.Load()
	if err != nil {
		return err
	}

	branch := store.GetBranch(branchName)
	if branch == nil {
		return fmt.Errorf("branch '%s' not found", branchName)
	}

	store.Current = branchName
	return m.metadata.Save(store)
}

func (m *Manager) DeleteBranch(ctx context.Context, branchName string) error {
	store, err := m.metadata.Load()
	if err != nil {
		return err
	}

	branch := store.GetBranch(branchName)
	if branch == nil {
		return fmt.Errorf("branch '%s' not found", branchName)
	}

	if branch.IsDefault {
		return fmt.Errorf("cannot delete default branch '%s'", branchName)
	}

	if store.Current == branchName {
		return fmt.Errorf("cannot delete current branch '%s'", branchName)
	}

	// Drop the schema
	if err := m.adapter.DropBranchSchema(ctx, branch.Schema); err != nil {
		return fmt.Errorf("failed to drop branch schema: %w", err)
	}

	// Remove from metadata
	if err := store.RemoveBranch(branchName); err != nil {
		return err
	}

	return m.metadata.Save(store)
}

func (m *Manager) RenameBranch(oldName, newName string) error {
	store, err := m.metadata.Load()
	if err != nil {
		return err
	}

	branch := store.GetBranch(oldName)
	if branch == nil {
		return fmt.Errorf("branch '%s' not found", oldName)
	}

	if store.GetBranch(newName) != nil {
		return fmt.Errorf("branch '%s' already exists", newName)
	}

	// Update branch name
	branch.Name = newName

	// Update current if needed
	if store.Current == oldName {
		store.Current = newName
	}

	return m.metadata.Save(store)
}

func (m *Manager) ListBranches() ([]*BranchMetadata, string, error) {
	store, err := m.metadata.Load()
	if err != nil {
		return nil, "", err
	}
	return store.Branches, store.Current, nil
}

func (m *Manager) GetCurrentBranch() (string, error) {
	store, err := m.metadata.Load()
	if err != nil {
		return "", err
	}
	return store.Current, nil
}

func (m *Manager) GetBranchSchema(branchName string) (string, error) {
	store, err := m.metadata.Load()
	if err != nil {
		return "", err
	}

	branch := store.GetBranch(branchName)
	if branch == nil {
		return "", fmt.Errorf("branch '%s' not found", branchName)
	}

	return branch.Schema, nil
}

func (m *Manager) generateSchemaName(branchName string) string {
	switch m.provider {
	case "postgresql", "postgres":
		return fmt.Sprintf("flash_branch_%s", branchName)
	case "mysql":
		return fmt.Sprintf("flash_branch_%s", branchName)
	case "sqlite", "sqlite3":
		// For SQLite, return just the branch name - the adapter will handle file path
		return branchName
	default:
		return fmt.Sprintf("flash_branch_%s", branchName)
	}
}

func (m *Manager) Close() error {
	if m.adapter != nil {
		return m.adapter.Close()
	}
	return nil
}
