package seeder

type SeedConfig struct {
	Count          int            // Default records per table
	Tables         map[string]int // Per-table counts (also used as the specific-tables filter when non-empty)
	Truncate       bool           // Clear tables before seeding
	Force          bool           // Skip confirmations and continue on errors
	DryRun         bool           // Print sample data without inserting
	Exclude        []string       // Tables to skip
	SpecificTables []string       // If set, only seed these tables (plus required FK parents)
}

type TableInfo struct {
	Name         string
	Columns      []ColumnInfo
	PrimaryKey   string
	ForeignKeys  []ForeignKey
	Dependencies []string
}

type ColumnInfo struct {
	Name     string
	Type     string
	Nullable bool
	IsPK     bool
	IsUnique bool
	IsFK     bool
	FKTable  string
	FKColumn string
}

type ForeignKey struct {
	Column    string
	RefTable  string
	RefColumn string
}

type GeneratedData struct {
	TableName   string
	Records     []map[string]any
	InsertedIDs map[string][]any // table -> list of IDs
}
