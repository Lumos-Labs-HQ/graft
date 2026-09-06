package parser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/utils"
)

// ParseJsonAnnotation parses a -- @json annotation line.
func ParseJsonAnnotation(line string, jsonBasePath string) (*JsonType, error) {
	// Strip the "-- @json" prefix
	content := strings.TrimPrefix(line, "-- @json")
	content = strings.TrimSpace(content)

	if content == "" {
		return nil, fmt.Errorf("empty @json annotation")
	}

	// Check if it's an import
	if strings.HasPrefix(content, "import ") {
		return parseJsonImport(content, jsonBasePath)
	}

	return parseJsonInline(content)
}

// parseJsonImport handles: import filename.json as column_name
func parseJsonImport(content string, jsonBasePath string) (*JsonType, error) {
	content = strings.TrimPrefix(content, "import ")
	content = strings.TrimSpace(content)

	parts := strings.SplitN(content, " as ", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("@json import must have 'as column_name', got: %q", content)
	}

	filePath := strings.TrimSpace(parts[0])
	columnName := strings.TrimSpace(parts[1])

	if filePath == "" || columnName == "" {
		return nil, fmt.Errorf("@json import: file path and column name are required")
	}

	// Resolve file path relative to json_path
	var fullPath string
	if filepath.IsAbs(filePath) {
		fullPath = filePath
	} else if jsonBasePath != "" {
		fullPath = filepath.Join(jsonBasePath, filePath)
	} else {
		fullPath = filePath
	}

	// Read and parse the JSON file
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("@json import: cannot read file %q: %w", fullPath, err)
	}

	fields, err := parseJsonFieldsFromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("@json import %q: %w", filePath, err)
	}

	return &JsonType{
		Column: columnName,
		Name:   utils.ToPascalCase(columnName),
		Fields: fields,
	}, nil
}

// parseJsonInline handles: column_name {"field": "type", ...}
func parseJsonInline(content string) (*JsonType, error) {
	// Find the start of the JSON object
	braceIdx := strings.Index(content, "{")
	if braceIdx == -1 {
		return nil, fmt.Errorf("@json inline: expected JSON object, got: %q", content)
	}

	columnName := strings.TrimSpace(content[:braceIdx])
	jsonStr := content[braceIdx:]

	if columnName == "" {
		return nil, fmt.Errorf("@json inline: column name is required before the JSON object")
	}

	fields, err := parseJsonFieldsFromBytes([]byte(jsonStr))
	if err != nil {
		return nil, fmt.Errorf("@json inline for %q: %w", columnName, err)
	}

	return &JsonType{
		Column: columnName,
		Name:   utils.ToPascalCase(columnName),
		Fields: fields,
	}, nil
}

// parseJsonFieldsFromBytes parses a JSON object where values are type strings.
func parseJsonFieldsFromBytes(data []byte) ([]*JsonField, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	return parseJsonFieldMap(raw)
}

func parseJsonFieldMap(raw map[string]any) ([]*JsonField, error) {
	fields := make([]*JsonField, 0, len(raw))

	for key, val := range raw {
		field := &JsonField{
			Name:     key,
			Nullable: true, // all JSON fields are nullable by default
		}

		switch v := val.(type) {
		case string:
			field.Type = normalizeJsonType(v)
		case map[string]any:
			// Nested object — type becomes the PascalCase of the field name
			field.Type = utils.ToPascalCase(key)
		default:
			field.Type = "string" // fallback
		}

		fields = append(fields, field)
	}

	return fields, nil
}

// normalizeJsonType normalizes user-provided type strings to canonical forms.
func normalizeJsonType(t string) string {
	t = strings.TrimSpace(strings.ToLower(t))

	switch t {
	case "string", "str", "text", "varchar":
		return "string"
	case "int", "integer", "int32", "int64", "long", "bigint":
		return "int"
	case "float", "float32", "float64", "double", "real", "numeric", "decimal", "number":
		return "float"
	case "bool", "boolean":
		return "boolean"
	case "string[]", "[]string", "text[]":
		return "string[]"
	case "int[]", "[]int", "integer[]":
		return "int[]"
	case "float[]", "[]float", "double[]", "number[]":
		return "float[]"
	case "boolean[]", "bool[]":
		return "boolean[]"
	case "any", "object", "json", "jsonb", "map":
		return "any"
	case "datetime", "timestamp", "date", "time":
		return "string" // dates in JSON are typically strings
	case "uuid":
		return "string" // UUIDs in JSON are typically strings
	default:
		// Could be a nested object type reference
		return t
	}
}

// GetNestedJsonTypes extracts any nested object definitions from a JsonType's fields.
// Returns additional JsonType definitions that need to be generated.
func GetNestedJsonTypes(jt *JsonType, rawData []byte) []*JsonType {
	var raw map[string]any
	if err := json.Unmarshal(rawData, &raw); err != nil {
		return nil
	}

	var nested []*JsonType
	for key, val := range raw {
		if obj, ok := val.(map[string]any); ok {
			nestedFields, _ := parseJsonFieldMap(obj)
			nested = append(nested, &JsonType{
				Column: key,
				Name:   utils.ToPascalCase(key),
				Fields: nestedFields,
			})
		}
	}
	return nested
}
