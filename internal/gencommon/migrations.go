package gencommon

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/database/common"
)

// EmbeddedMigration is the generation-time representation used to produce a
// binary-independent migration runner in generated clients.
type EmbeddedMigration struct {
	ID         string
	Name       string
	Checksum   string
	Statements []string
}

// LoadEmbeddedMigrations reads, orders, and splits the UP section of migration
// files. A missing migrations directory is valid for projects without a first
// migration yet.
func LoadEmbeddedMigrations(dir string) ([]EmbeddedMigration, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	var paths []string
	if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && (strings.HasSuffix(strings.ToLower(entry.Name()), ".sql") || strings.HasSuffix(strings.ToLower(entry.Name()), ".cql")) {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk migrations: %w", err)
	}
	slices.Sort(paths)

	migrations := make([]EmbeddedMigration, 0, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", path, err)
		}
		id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		checksum := sha256.Sum256(content)
		migrations = append(migrations, EmbeddedMigration{
			ID:         id,
			Name:       migrationName(id),
			Checksum:   fmt.Sprintf("%x", checksum),
			Statements: common.ParseSQLStatements(extractMigrationUp(string(content))),
		})
	}
	return migrations, nil
}

func migrationName(id string) string {
	if len(id) > 15 && id[14] == '_' {
		return id[15:]
	}
	return id
}

func extractMigrationUp(content string) string {
	lines := strings.Split(content, "\n")
	up := make([]string, 0, len(lines))
	inUp := false
	hasMarkers := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(trimmed, "--") && strings.Contains(lower, "+migrate up") {
			inUp = true
			hasMarkers = true
			continue
		}
		if strings.HasPrefix(trimmed, "--") && strings.Contains(lower, "+migrate down") {
			inUp = false
			continue
		}
		if inUp {
			up = append(up, line)
		}
	}
	if !hasMarkers {
		return content
	}
	return strings.Join(up, "\n")
}
