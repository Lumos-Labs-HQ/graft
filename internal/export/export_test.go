package export

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

func makeBackup(tables map[string][]map[string]any) types.BackupData {
	t := make(map[string]any, len(tables))
	for k, v := range tables {
		t[k] = v
	}
	return types.BackupData{
		Timestamp: "2024-01-01 00:00:00",
		Version:   "1.0",
		Tables:    t,
		Comment:   "test",
	}
}

func TestExportToJSON_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	data := makeBackup(map[string][]map[string]any{
		"users": {{"id": 1, "email": "a@b.com"}},
	})

	path, err := exportToJSON(data, dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("exportToJSON error: %v", err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("output file not created: %s", path)
	}
	if !strings.HasSuffix(path, ".json") {
		t.Errorf("expected .json extension, got %q", path)
	}
}

func TestExportToJSON_ValidJSON(t *testing.T) {
	dir := t.TempDir()
	data := makeBackup(map[string][]map[string]any{
		"users": {{"id": 1, "name": "Alice"}},
	})

	path, err := exportToJSON(data, dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("exportToJSON error: %v", err)
	}

	raw, _ := os.ReadFile(path)
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Errorf("output is not valid JSON: %v", err)
	}
}

func TestExportToJSON_EmptyTables(t *testing.T) {
	dir := t.TempDir()
	data := makeBackup(nil)
	path, err := exportToJSON(data, dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("exportToJSON error: %v", err)
	}
	if path == "" {
		t.Error("expected non-empty path even for empty data")
	}
}

func TestExportToJSON_CreatesExportDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "nested", "dir")
	data := makeBackup(nil)
	_, err := exportToJSON(data, dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("should create nested dirs: %v", err)
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Error("export directory not created")
	}
}
