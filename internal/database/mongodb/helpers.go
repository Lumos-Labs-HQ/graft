package mongodb

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// inferBSONType infers the MongoDB type from a Go value
func inferBSONType(value any) string {
	switch v := value.(type) {
	case string:
		return "string"
	case int, int32, int64:
		return "int"
	case float32, float64:
		return "double"
	case bool:
		return "bool"
	case bson.M, map[string]any:
		return "object"
	case bson.A, []any:
		return "array"
	case time.Time:
		return "date"
	case primitive.ObjectID:
		return "ObjectId"
	case primitive.DateTime:
		return "date"
	case primitive.Decimal128:
		return "decimal"
	case primitive.Binary:
		return "binData"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// convertBSONValue converts BSON values to standard Go types in-place for maps/slices
func convertBSONValue(v any) any {
	switch val := v.(type) {
	case bson.M:
		// Mutate in-place to avoid allocating a new map
		for k, v := range val {
			val[k] = convertBSONValue(v)
		}
		return val
	case bson.A:
		// Mutate in-place to avoid allocating a new slice
		for i, v := range val {
			val[i] = convertBSONValue(v)
		}
		return val
	case bson.D:
		result := make(map[string]any, len(val))
		for _, elem := range val {
			result[elem.Key] = convertBSONValue(elem.Value)
		}
		return result
	case primitive.ObjectID:
		return val.Hex()
	case primitive.DateTime:
		return val.Time().UTC().Format(time.RFC3339)
	case primitive.Decimal128:
		return val.String()
	case primitive.Binary:
		return "<Binary(" + strconv.Itoa(len(val.Data)) + " bytes)>"
	case string:
		// Truncate very large strings to prevent massive JSON/HTML rendering
		if len(val) > 10000 {
			return val[:10000] + "... (" + strconv.Itoa(len(val)-10000) + " more chars)"
		}
		return val
	default:
		return v
	}
}

// extractBetweenBalanced extracts a substring between start and end delimiters,
// respecting nested matching pairs. This correctly handles nested braces/objects.
func extractBetweenBalanced(str, start, end string) string {
	startIdx := strings.Index(str, start)
	if startIdx == -1 {
		return ""
	}
	startIdx += len(start)

	depth := 1
	for i := startIdx; i < len(str); i++ {
		if strings.HasPrefix(str[i:], start) {
			depth++
			i += len(start) - 1
		} else if strings.HasPrefix(str[i:], end) {
			depth--
			if depth == 0 {
				return strings.TrimSpace(str[startIdx:i])
			}
			i += len(end) - 1
		}
	}
	return ""
}

// parseObjectID parses a string ID to ObjectID or returns the string as-is
func parseObjectID(id string) (any, error) {
	if len(id) == 24 {
		oid, err := primitive.ObjectIDFromHex(id)
		if err != nil {
			return nil, fmt.Errorf("invalid ObjectID: %w", err)
		}
		return oid, nil
	}
	return id, nil
}
