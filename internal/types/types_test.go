package types

import (
	"testing"
	"time"
)

func TestSchemaColumn_ZeroValue(t *testing.T) {
	var c SchemaColumn
	if c.Nullable != false || c.IsPrimary != false || c.IsUnique != false {
		t.Errorf("unexpected zero value: %+v", c)
	}
}

func TestSchemaDiff_IsEmpty(t *testing.T) {
	d := SchemaDiff{}
	if len(d.NewTables) != 0 || len(d.DroppedTables) != 0 || len(d.ModifiedTables) != 0 {
		t.Errorf("zero SchemaDiff should be empty: %+v", d)
	}
}

func TestMigration_Fields(t *testing.T) {
	now := time.Now()
	m := Migration{
		ID:        "20240101_init",
		Name:      "init",
		Applied:   true,
		AppliedAt: &now,
		FilePath:  "db/migrations/20240101_init.sql",
		Checksum:  "abc123",
	}
	if m.ID != "20240101_init" || !m.Applied || m.AppliedAt == nil {
		t.Errorf("Migration fields = %+v", m)
	}
}

func TestMigrationStatus_Fields(t *testing.T) {
	s := MigrationStatus{TotalMigrations: 5, AppliedMigrations: 3, PendingMigrations: 2}
	if s.TotalMigrations != 5 || s.AppliedMigrations != 3 || s.PendingMigrations != 2 {
		t.Errorf("MigrationStatus = %+v", s)
	}
}

func TestSchemaEnum_Fields(t *testing.T) {
	e := SchemaEnum{Name: "status", Values: []string{"active", "inactive"}}
	if e.Name != "status" || len(e.Values) != 2 {
		t.Errorf("SchemaEnum = %+v", e)
	}
}

func TestSchemaIndex_Fields(t *testing.T) {
	idx := SchemaIndex{Name: "idx_email", Table: "users", Columns: []string{"email"}, Unique: true}
	if idx.Name != "idx_email" || !idx.Unique || len(idx.Columns) != 1 {
		t.Errorf("SchemaIndex = %+v", idx)
	}
}

func TestTableDiff_Fields(t *testing.T) {
	td := TableDiff{
		Name:       "users",
		NewColumns: []SchemaColumn{{Name: "phone", Type: "TEXT"}},
	}
	if td.Name != "users" || len(td.NewColumns) != 1 {
		t.Errorf("TableDiff = %+v", td)
	}
}

func TestColumnDiff_Fields(t *testing.T) {
	cd := ColumnDiff{Name: "email", OldType: "VARCHAR(100)", NewType: "TEXT", Changes: []string{"type changed"}}
	if cd.Name != "email" || len(cd.Changes) != 1 {
		t.Errorf("ColumnDiff = %+v", cd)
	}
}

func TestBackupData_Fields(t *testing.T) {
	bd := BackupData{
		Timestamp: "2024-01-01",
		Version:   "1.0",
		Tables:    map[string]any{"users": []map[string]any{{"id": 1}}},
		Comment:   "test backup",
	}
	if bd.Version != "1.0" || len(bd.Tables) != 1 {
		t.Errorf("BackupData = %+v", bd)
	}
}

func TestMigrationConflict_Fields(t *testing.T) {
	mc := MigrationConflict{
		Type:        "column_exists",
		TableName:   "users",
		ColumnName:  "email",
		Description: "column already exists",
		Severity:    "error",
	}
	if mc.TableName != "users" || mc.Severity != "error" {
		t.Errorf("MigrationConflict = %+v", mc)
	}
}
