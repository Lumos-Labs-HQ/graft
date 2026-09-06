package validation

import (
	"reflect"
	"strings"
)

// SchemaColumn represents a column that can be validated
type SchemaColumn interface {
	GetName() string
}

// SchemaTable represents a table that can be validated
type SchemaTable interface {
	GetName() string
	GetColumns() []SchemaColumn
}

// SimpleColumn is a wrapper for basic column validation
type SimpleColumn struct {
	Name string
}

func (c SimpleColumn) GetName() string { return c.Name }

// SimpleTable is a wrapper for basic table validation
type SimpleTable struct {
	Name    string
	Columns []SimpleColumn
}

func (t SimpleTable) GetName() string { return t.Name }
func (t SimpleTable) GetColumns() []SchemaColumn {
	cols := make([]SchemaColumn, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = c
	}
	return cols
}

// extractTableNamesFromSchema extracts table names from various schema types
func extractTableNamesFromSchema(schema any) map[string]bool {
	if s, ok := schema.(interface {
		GetTables() []interface{ GetName() string }
	}); ok {
		tables := s.GetTables()
		names := make(map[string]bool, len(tables))
		for _, t := range tables {
			names[strings.ToLower(t.GetName())] = true
		}
		return names
	}

	switch s := schema.(type) {
	case interface{ GetTableNames() map[string]bool }:
		return s.GetTableNames()
	default:
		return extractTableNamesViaReflection(s)
	}
}

// extractTableNamesViaReflection is the fallback that uses reflection
func extractTableNamesViaReflection(schema any) map[string]bool {
	schemaVal := reflect.ValueOf(schema)
	if schemaVal.Kind() == reflect.Pointer {
		schemaVal = schemaVal.Elem()
	}

	if schemaVal.Kind() != reflect.Struct {
		return nil
	}

	tablesField := schemaVal.FieldByName("Tables")
	if !tablesField.IsValid() || tablesField.Kind() != reflect.Slice {
		return nil
	}

	tableNames := make(map[string]bool, tablesField.Len())
	for i := 0; i < tablesField.Len(); i++ {
		tablePtr := tablesField.Index(i)
		if tablePtr.Kind() == reflect.Pointer {
			tablePtr = tablePtr.Elem()
		}
		if tablePtr.Kind() == reflect.Struct {
			nameField := tablePtr.FieldByName("Name")
			if nameField.IsValid() && nameField.Kind() == reflect.String {
				tableNames[strings.ToLower(nameField.String())] = true
			}
		}
	}
	return tableNames
}
