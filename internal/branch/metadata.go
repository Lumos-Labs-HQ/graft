package branch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type BranchMetadata struct {
	Name      string    `json:"name"`
	Parent    string    `json:"parent"`
	Schema    string    `json:"schema"`
	CreatedAt time.Time `json:"created_at"`
	IsDefault bool      `json:"is_default"`
}

type BranchStore struct {
	Current  string            `json:"current"`
	Branches []*BranchMetadata `json:"branches"`
}

type MetadataManager struct {
	filePath string
}

func NewMetadataManager(migrationsPath string) *MetadataManager {
	flashDir := filepath.Join(migrationsPath, ".flash")
	os.MkdirAll(flashDir, 0755)
	
	return &MetadataManager{
		filePath: filepath.Join(flashDir, "branches.json"),
	}
}

func (m *MetadataManager) Load() (*BranchStore, error) {
	if _, err := os.Stat(m.filePath); os.IsNotExist(err) {
		return m.initDefault("public"), nil
	}

	data, err := os.ReadFile(m.filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read branches file: %w", err)
	}

	var store BranchStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("failed to parse branches file: %w", err)
	}

	return &store, nil
}

func (m *MetadataManager) Save(store *BranchStore) error {
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal branches: %w", err)
	}

	return os.WriteFile(m.filePath, data, 0644)
}

func (m *MetadataManager) initDefault(defaultSchema string) *BranchStore {
	return &BranchStore{
		Current: "main",
		Branches: []*BranchMetadata{
			{
				Name:      "main",
				Parent:    "",
				Schema:    defaultSchema,
				CreatedAt: time.Now(),
				IsDefault: true,
			},
		},
	}
}

func (s *BranchStore) GetBranch(name string) *BranchMetadata {
	for _, b := range s.Branches {
		if b.Name == name {
			return b
		}
	}
	return nil
}

func (s *BranchStore) AddBranch(branch *BranchMetadata) error {
	if s.GetBranch(branch.Name) != nil {
		return fmt.Errorf("branch '%s' already exists", branch.Name)
	}
	s.Branches = append(s.Branches, branch)
	return nil
}

func (s *BranchStore) RemoveBranch(name string) error {
	for i, b := range s.Branches {
		if b.Name == name {
			s.Branches = append(s.Branches[:i], s.Branches[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("branch '%s' not found", name)
}
