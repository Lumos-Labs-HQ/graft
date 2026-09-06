package gencommon

import "strings"

// ExtractEnumValues extracts values from a MySQL inline ENUM column type like "enum('a','b')".
func ExtractEnumValues(columnType string) []string {
	// Test the "enum(" prefix case-insensitively, but slice the values out of the
	// ORIGINAL string so their case is preserved — a column declared
	// enum('Active','Pending') must yield 'Active'/'Pending', not 'active'/'pending'.
	if !strings.HasPrefix(strings.ToLower(columnType), "enum(") {
		return nil
	}

	values := columnType[5 : len(columnType)-1]

	var result []string
	parts := strings.SplitSeq(values, ",")
	for part := range parts {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, "'\"")
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}
