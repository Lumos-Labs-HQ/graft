package schema

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

// SchemaSnapshot is a JSON-serialized representation of the database schema
// at a specific point in time. It lives next to the migrations folder and is
// updated every time a migration is generated. This lets the generator diff
// against the *last known schema* instead of the live DB, which solves the
// "unapplied migration" edge case cleanly.
type SchemaSnapshot struct {
	Version       string                 `json:"version"`
	GeneratedAt   time.Time              `json:"generated_at"`
	Tables        []types.SchemaTable    `json:"tables"`
	Enums         []types.SchemaEnum     `json:"enums"`
	Indexes       []types.SchemaIndex    `json:"indexes"`
	Keyspaces     []types.SchemaKeyspace `json:"keyspaces"`
	UDTs          []types.SchemaUDT      `json:"udts"`
	RawStatements []string               `json:"raw_statements,omitempty"`
}

const snapshotVersion = "2"
const defaultSnapshotFileName = "schema_snapshot.json"

// SnapshotPath returns the default snapshot path inside a migrations directory.
func SnapshotPath(migrationsDir string) string {
	return filepath.Join(migrationsDir, ".flash", defaultSnapshotFileName)
}

// LoadSchemaSnapshot reads a snapshot from disk. If the file does not exist
// or is unreadable, it returns (nil, nil) so the caller can fall back to the
// live database.
func LoadSchemaSnapshot(path string) (*SchemaSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read schema snapshot: %w", err)
	}

	var snap SchemaSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("failed to parse schema snapshot: %w", err)
	}

	// Basic validation
	if snap.Version == "" {
		return nil, fmt.Errorf("schema snapshot missing version")
	}

	return &snap, nil
}

// SaveSchemaSnapshot writes the given schema state to disk.
func SaveSchemaSnapshot(path string, tables []types.SchemaTable, enums []types.SchemaEnum, indexes ...[]types.SchemaIndex) error {
	return SaveSchemaSnapshotFull(path, tables, enums, indexes, nil)
}

// SaveSchemaSnapshotFull writes the given schema state including keyspaces.
func SaveSchemaSnapshotFull(path string, tables []types.SchemaTable, enums []types.SchemaEnum, indexes any, keyspaces []types.SchemaKeyspace) error {
	return SaveSchemaSnapshotFullV2(path, tables, enums, indexes, keyspaces, nil)
}

func SaveSchemaSnapshotFullV2(path string, tables []types.SchemaTable, enums []types.SchemaEnum, indexes any, keyspaces []types.SchemaKeyspace, udts []types.SchemaUDT) error {
	return SaveSchemaSnapshotFullV3(path, tables, enums, indexes, keyspaces, udts, nil)
}

func SaveSchemaSnapshotFullV3(path string, tables []types.SchemaTable, enums []types.SchemaEnum, indexes any, keyspaces []types.SchemaKeyspace, udts []types.SchemaUDT, rawStatements []string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create snapshot directory: %w", err)
	}

	snap := SchemaSnapshot{
		Version:       snapshotVersion,
		GeneratedAt:   time.Now().UTC(),
		Tables:        tables,
		Enums:         enums,
		Keyspaces:     keyspaces,
		UDTs:          udts,
		RawStatements: rawStatements,
	}
	switch v := indexes.(type) {
	case []types.SchemaIndex:
		if len(v) > 0 {
			snap.Indexes = v
		}
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal schema snapshot: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write schema snapshot: %w", err)
	}

	return nil
}
