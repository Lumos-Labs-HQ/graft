package branch

import (
	"context"
	"fmt"

	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

type SchemaDiff struct {
	TablesAdded   []string
	TablesRemoved []string
	TablesChanged []TableDiff
}

type TableDiff struct {
	Name           string
	ColumnsAdded   []string
	ColumnsRemoved []string
	ColumnsChanged []string
}

func (m *Manager) GetSchemaDiff(ctx context.Context, branch1, branch2 string) (*SchemaDiff, error) {
	store, err := m.metadata.Load()
	if err != nil {
		return nil, err
	}

	b1 := store.GetBranch(branch1)
	b2 := store.GetBranch(branch2)

	if b1 == nil {
		return nil, fmt.Errorf("branch '%s' not found", branch1)
	}
	if b2 == nil {
		return nil, fmt.Errorf("branch '%s' not found", branch2)
	}

	schema1, err := m.getSchemaForBranch(ctx, b1.Schema)
	if err != nil {
		return nil, fmt.Errorf("failed to get schema for %s: %w", branch1, err)
	}

	schema2, err := m.getSchemaForBranch(ctx, b2.Schema)
	if err != nil {
		return nil, fmt.Errorf("failed to get schema for %s: %w", branch2, err)
	}

	return m.compareSchemas(schema1, schema2), nil
}

func (m *Manager) getSchemaForBranch(ctx context.Context, schemaName string) ([]types.SchemaTable, error) {
	// Temporarily switch context to the schema
	// This is a simplified version - production would need proper schema context handling
	return m.adapter.GetCurrentSchema(ctx)
}

func (m *Manager) compareSchemas(schema1, schema2 []types.SchemaTable) *SchemaDiff {
	diff := &SchemaDiff{}

	table1Map := make(map[string]types.SchemaTable)
	table2Map := make(map[string]types.SchemaTable)

	for _, t := range schema1 {
		table1Map[t.Name] = t
	}
	for _, t := range schema2 {
		table2Map[t.Name] = t
	}

	// Find added tables (in schema2 but not in schema1)
	for name := range table2Map {
		if _, exists := table1Map[name]; !exists {
			diff.TablesAdded = append(diff.TablesAdded, name)
		}
	}

	// Find removed tables (in schema1 but not in schema2)
	for name := range table1Map {
		if _, exists := table2Map[name]; !exists {
			diff.TablesRemoved = append(diff.TablesRemoved, name)
		}
	}

	// Find changed tables
	for name, t1 := range table1Map {
		if t2, exists := table2Map[name]; exists {
			if tableDiff := m.compareTableColumns(t1, t2); tableDiff != nil {
				diff.TablesChanged = append(diff.TablesChanged, *tableDiff)
			}
		}
	}

	return diff
}

func (m *Manager) compareTableColumns(table1, table2 types.SchemaTable) *TableDiff {
	col1Map := make(map[string]types.SchemaColumn)
	col2Map := make(map[string]types.SchemaColumn)

	for _, c := range table1.Columns {
		col1Map[c.Name] = c
	}
	for _, c := range table2.Columns {
		col2Map[c.Name] = c
	}

	diff := &TableDiff{Name: table1.Name}
	hasChanges := false

	// Find added columns
	for name := range col2Map {
		if _, exists := col1Map[name]; !exists {
			diff.ColumnsAdded = append(diff.ColumnsAdded, name)
			hasChanges = true
		}
	}

	// Find removed columns
	for name := range col1Map {
		if _, exists := col2Map[name]; !exists {
			diff.ColumnsRemoved = append(diff.ColumnsRemoved, name)
			hasChanges = true
		}
	}

	// Find changed columns (type or constraints changed)
	for name, c1 := range col1Map {
		if c2, exists := col2Map[name]; exists {
			if c1.Type != c2.Type || c1.Nullable != c2.Nullable {
				diff.ColumnsChanged = append(diff.ColumnsChanged, name)
				hasChanges = true
			}
		}
	}

	if !hasChanges {
		return nil
	}

	return diff
}

func (d *SchemaDiff) IsEmpty() bool {
	return len(d.TablesAdded) == 0 &&
		len(d.TablesRemoved) == 0 &&
		len(d.TablesChanged) == 0
}

func (d *SchemaDiff) String() string {
	if d.IsEmpty() {
		return "No differences found"
	}

	result := ""

	if len(d.TablesAdded) > 0 {
		result += "Tables added:\n"
		for _, t := range d.TablesAdded {
			result += fmt.Sprintf("  + %s\n", t)
		}
	}

	if len(d.TablesRemoved) > 0 {
		result += "Tables removed:\n"
		for _, t := range d.TablesRemoved {
			result += fmt.Sprintf("  - %s\n", t)
		}
	}

	if len(d.TablesChanged) > 0 {
		result += "Tables modified:\n"
		for _, t := range d.TablesChanged {
			result += fmt.Sprintf("  ~ %s\n", t.Name)
			for _, c := range t.ColumnsAdded {
				result += fmt.Sprintf("      + column: %s\n", c)
			}
			for _, c := range t.ColumnsRemoved {
				result += fmt.Sprintf("      - column: %s\n", c)
			}
			for _, c := range t.ColumnsChanged {
				result += fmt.Sprintf("      ~ column: %s\n", c)
			}
		}
	}

	return result
}
