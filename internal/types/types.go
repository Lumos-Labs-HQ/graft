package types

import (
	"time"
)

type SchemaEnum struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

type SchemaTable struct {
	Name        string
	Columns     []SchemaColumn
	Indexes     []SchemaIndex
	CompositePK string // raw CQL PRIMARY KEY clause, e.g. "((country, city), order_id)"
	Suffix      string // e.g. "PARTITION BY RANGE (changed_at)" — appended after closing paren
}

type SchemaColumn struct {
	Name             string
	Type             string
	Nullable         bool
	Default          string
	IsPrimary        bool
	IsUnique         bool
	IsAutoIncrement  bool
	ForeignKeyTable  string
	ForeignKeyColumn string
	OnDeleteAction   string
	OnUpdateAction   string // ON UPDATE action for FK
	Check            string
	Generated        string // GENERATED ALWAYS AS expression
	IsIdentity       bool   // GENERATED ALWAYS AS IDENTITY (PostgreSQL)
}

type SchemaIndex struct {
	Name    string
	Table   string
	Columns []string
	Unique  bool
	Where   string   // Partial index WHERE clause
	Method  string   // USING btree|hash|gin|gist|brin|spgist (PostgreSQL)
	Expr    []string // Expression index columns e.g. lower(email)
}

type SchemaConstraint struct {
	Name    string
	Table   string
	Type    string // CHECK, UNIQUE, EXCLUDE
	Expr    string // for CHECK constraints
	Columns []string
}

type SchemaKeyspace struct {
	Name          string
	Replication   string
	DurableWrites *bool
}

type SchemaDiff struct {
	NewTables          []SchemaTable
	DroppedTables      []string
	ModifiedTables     []TableDiff
	NewIndexes         []SchemaIndex
	DroppedIndexes     []SchemaIndex
	NewEnums           []SchemaEnum
	DroppedEnums       []string
	ModifiedEnums      []EnumDiff
	RenamedColumns     []RenameOp
	RenamedTables      []RenameOp
	NewConstraints     []SchemaConstraint
	DroppedConstraints []SchemaConstraint
	NewKeyspaces       []SchemaKeyspace
	DroppedKeyspaces   []string
	NewUDTs            []SchemaUDT // CQL user-defined types (ScyllaDB/Cassandra)
	DroppedUDTs        []string    // CQL UDT names to drop
	NewRawStatements   []string    // Raw SQL statements (DOMAIN, PARTITION OF, composite types, triggers, functions)
}

type SchemaUDT struct {
	Name   string
	Fields []SchemaUDTField
}

type SchemaUDTField struct {
	Name string
	Type string
}

type EnumDiff struct {
	Name      string
	AddValues []string
}

type RenameOp struct {
	Table   string
	OldName string
	NewName string
}

type TableDiff struct {
	Name            string
	NewColumns      []SchemaColumn
	DroppedColumns  []SchemaColumn
	ModifiedColumns []ColumnDiff
	RenamedColumns  []RenameOp
	OldTable        SchemaTable
	NewTable        SchemaTable
}

type ColumnDiff struct {
	Name             string
	OldType          string
	NewType          string
	Changes          []string
	OldColumn        SchemaColumn
	NewColumn        SchemaColumn
	NullableChanged  bool
	DefaultChanged   bool
	GeneratedChanged bool
}

type MigrationConflict struct {
	Type        string
	TableName   string
	ColumnName  string
	Description string
	Solutions   []string
	Severity    string
}

type Migration struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Applied   bool       `json:"applied"`
	AppliedAt *time.Time `json:"applied_at,omitempty"`
	FilePath  string     `json:"file_path"`
	Checksum  string     `json:"checksum"`
	CreatedAt time.Time  `json:"created_at"`
}

type MigrationSQL struct {
	Up   string
	Down string
}

type MigrationFile struct {
	ID       string
	Name     string
	Up       string
	Down     string
	Checksum string
	FilePath string
}

type BackupData struct {
	Timestamp string         `json:"timestamp"`
	Version   string         `json:"version"`
	Tables    map[string]any `json:"tables"`
	Comment   string         `json:"comment"`
}

type MigrationStatusItem struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	AppliedAt *time.Time `json:"applied_at,omitempty"`
}

type MigrationStatus struct {
	TotalMigrations   int `json:"total_migrations"`
	AppliedMigrations int `json:"applied_migrations"`
	PendingMigrations int `json:"pending_migrations"`
}
