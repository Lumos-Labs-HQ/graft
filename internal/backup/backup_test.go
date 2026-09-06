package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

// ── writeBackupToFile ─────────────────────────────────────────────────────────

func TestWriteBackupToFile_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	data := types.BackupData{
		Timestamp: "2024-01-01 00:00:00",
		Version:   "1.0",
		Tables:    map[string]any{"users": []map[string]any{{"id": 1}}},
		Comment:   "test",
	}

	path, err := writeBackupToFile(data, dir)
	if err != nil {
		t.Fatalf("writeBackupToFile error: %v", err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("backup file not created: %s", path)
	}
	if !strings.HasSuffix(path, ".json") {
		t.Errorf("expected .json extension, got %q", path)
	}
}

func TestWriteBackupToFile_ValidJSON(t *testing.T) {
	dir := t.TempDir()
	data := types.BackupData{
		Timestamp: "2024-01-01 00:00:00",
		Version:   "1.0",
		Tables:    map[string]any{"users": []map[string]any{{"id": 1, "name": "Alice"}}},
		Comment:   "unit test",
	}

	path, err := writeBackupToFile(data, dir)
	if err != nil {
		t.Fatalf("writeBackupToFile error: %v", err)
	}

	raw, _ := os.ReadFile(path)
	var parsed types.BackupData
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Errorf("output is not valid JSON: %v", err)
	}
	if parsed.Comment != "unit test" {
		t.Errorf("Comment = %q, want 'unit test'", parsed.Comment)
	}
}

func TestWriteBackupToFile_CreatesNestedDirs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deep", "nested", "backup")
	data := types.BackupData{Tables: map[string]any{}}

	_, err := writeBackupToFile(data, dir)
	if err != nil {
		t.Fatalf("should create nested dirs: %v", err)
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Error("nested backup directory not created")
	}
}

func TestWriteBackupToFile_EmptyTables(t *testing.T) {
	dir := t.TempDir()
	data := types.BackupData{
		Timestamp: "2024-01-01",
		Version:   "1.0",
		Tables:    map[string]any{},
	}
	path, err := writeBackupToFile(data, dir)
	if err != nil {
		t.Fatalf("writeBackupToFile error: %v", err)
	}
	if path == "" {
		t.Error("expected non-empty path")
	}
}
