package seeder

import (
	"strings"
	"testing"
	"time"
)

// ── DataGenerator ─────────────────────────────────────────────────────────────

func newGen(t *testing.T) *DataGenerator {
	t.Helper()
	g, err := NewDataGenerator()
	if err != nil {
		t.Fatalf("NewDataGenerator: %v", err)
	}
	return g
}

func TestDataGenerator_Generate_Int(t *testing.T) {
	g := newGen(t)
	v := g.Generate("INTEGER", false)
	if _, ok := v.(int); !ok {
		t.Errorf("Generate(INTEGER) = %T, want int", v)
	}
}

func TestDataGenerator_Generate_Text(t *testing.T) {
	g := newGen(t)
	v := g.Generate("TEXT", false)
	if _, ok := v.(string); !ok {
		t.Errorf("Generate(TEXT) = %T, want string", v)
	}
}

func TestDataGenerator_Generate_Bool(t *testing.T) {
	g := newGen(t)
	v := g.Generate("BOOLEAN", false)
	if _, ok := v.(bool); !ok {
		t.Errorf("Generate(BOOLEAN) = %T, want bool", v)
	}
}

func TestDataGenerator_Generate_Float(t *testing.T) {
	g := newGen(t)
	v := g.Generate("DECIMAL", false)
	if _, ok := v.(float64); !ok {
		t.Errorf("Generate(DECIMAL) = %T, want float64", v)
	}
}

func TestDataGenerator_Generate_UUID(t *testing.T) {
	g := newGen(t)
	v := g.Generate("UUID", false)
	s, ok := v.(string)
	if !ok {
		t.Fatalf("Generate(UUID) = %T, want string", v)
	}
	if len(strings.Split(s, "-")) != 5 {
		t.Errorf("UUID format invalid: %q", s)
	}
	// RFC 4122 v4: 13th char should be '4'
	if s[14] != '4' {
		t.Errorf("UUID not v4 (13th char != '4'): %q", s)
	}
}

func TestDataGenerator_Generate_Nullable_CanBeNil(t *testing.T) {
	g := newGen(t)
	gotNil := false
	for range 100 {
		if g.Generate("TEXT", true) == nil {
			gotNil = true
			break
		}
	}
	if !gotNil {
		t.Error("nullable column never returned nil in 100 attempts")
	}
}

func TestDataGenerator_Generate_BigInt(t *testing.T) {
	g := newGen(t)
	v := g.Generate("BIGINT", false)
	if _, ok := v.(int64); !ok {
		t.Errorf("Generate(BIGINT) = %T, want int64", v)
	}
}

func TestDataGenerator_Generate_SmallInt(t *testing.T) {
	g := newGen(t)
	v := g.Generate("SMALLINT", false)
	if _, ok := v.(int); !ok {
		t.Errorf("Generate(SMALLINT) = %T, want int", v)
	}
}

func TestDataGenerator_Generate_Double(t *testing.T) {
	g := newGen(t)
	v := g.Generate("DOUBLE PRECISION", false)
	if _, ok := v.(float64); !ok {
		t.Errorf("Generate(DOUBLE PRECISION) = %T, want float64", v)
	}
}

func TestDataGenerator_Generate_Jsonb(t *testing.T) {
	g := newGen(t)
	v := g.Generate("JSONB", false)
	if _, ok := v.(string); !ok {
		t.Errorf("Generate(JSONB) = %T, want string", v)
	}
}

func TestDataGenerator_Generate_Enum(t *testing.T) {
	g := newGen(t)
	v := g.Generate("ENUM('active','inactive','pending')", false)
	s, ok := v.(string)
	if !ok {
		t.Fatalf("Generate(ENUM) = %T, want string", v)
	}
	if s != "active" && s != "inactive" && s != "pending" {
		t.Errorf("Generate(ENUM) = %q, want one of active/inactive/pending", s)
	}
}

func TestDataGenerator_Generate_DATE(t *testing.T) {
	g := newGen(t)
	v := g.Generate("DATE", false)
	if _, ok := v.(time.Time); !ok {
		t.Errorf("Generate(DATE) = %T, want time.Time", v)
	}
}

func TestDataGenerator_GenerateForColumn_Email(t *testing.T) {
	g := newGen(t)
	v := g.GenerateForColumn("email", "TEXT", false, nil)
	s, ok := v.(string)
	if !ok {
		t.Fatalf("email column = %T, want string", v)
	}
	if !strings.Contains(s, "@") {
		t.Errorf("email = %q, missing @", s)
	}
}

func TestDataGenerator_GenerateForColumn_Username(t *testing.T) {
	g := newGen(t)
	v := g.GenerateForColumn("username", "VARCHAR", false, nil)
	if _, ok := v.(string); !ok {
		t.Errorf("username column = %T, want string", v)
	}
}

func TestDataGenerator_GenerateForColumn_Password(t *testing.T) {
	g := newGen(t)
	v := g.GenerateForColumn("password", "VARCHAR", false, nil)
	s, ok := v.(string)
	if !ok {
		t.Fatalf("password column = %T, want string", v)
	}
	if len(s) == 0 {
		t.Error("password is empty")
	}
}

func TestDataGenerator_GenerateForColumn_Phone(t *testing.T) {
	g := newGen(t)
	v := g.GenerateForColumn("phone", "TEXT", false, nil)
	s, ok := v.(string)
	if !ok {
		t.Fatalf("phone column = %T, want string", v)
	}
	if len(s) == 0 {
		t.Error("phone is empty")
	}
}

func TestDataGenerator_GenerateForColumn_Document_IsNil(t *testing.T) {
	g := newGen(t)
	for range 20 {
		if v := g.GenerateForColumn("document", "TEXT", false, nil); v != nil {
			t.Errorf("document column = %v, want nil", v)
			break
		}
	}
}

func TestDataGenerator_GenerateForColumn_Status(t *testing.T) {
	g := newGen(t)
	v := g.GenerateForColumn("status", "TEXT", false, nil)
	if _, ok := v.(string); !ok {
		t.Errorf("status column = %T, want string", v)
	}
}

func TestDataGenerator_GenerateForColumn_IsActive_Boolean(t *testing.T) {
	g := newGen(t)
	v := g.GenerateForColumn("is_active", "BOOLEAN", false, nil)
	if _, ok := v.(bool); !ok {
		t.Errorf("is_active column = %T, want bool", v)
	}
}

func TestDataGenerator_GenerateForColumn_HasPermission_Boolean(t *testing.T) {
	g := newGen(t)
	v := g.GenerateForColumn("has_permission", "TINYINT", false, nil)
	if _, ok := v.(bool); !ok {
		t.Errorf("has_permission column = %T, want bool", v)
	}
}

func TestDataGenerator_GenerateForColumn_CreatedAt(t *testing.T) {
	g := newGen(t)
	v := g.GenerateForColumn("created_at", "TIMESTAMP", false, nil)
	if _, ok := v.(time.Time); !ok {
		t.Errorf("created_at column = %T, want time.Time", v)
	}
}

func TestDataGenerator_GenerateForColumn_IP(t *testing.T) {
	g := newGen(t)
	v := g.GenerateForColumn("ip_address", "VARCHAR", false, nil)
	s, ok := v.(string)
	if !ok {
		t.Fatalf("ip_address column = %T, want string", v)
	}
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		t.Errorf("ip_address = %q, want 4 octets", s)
	}
}

func TestDataGenerator_GenerateForColumn_Color(t *testing.T) {
	g := newGen(t)
	v := g.GenerateForColumn("color", "VARCHAR", false, nil)
	s, ok := v.(string)
	if !ok {
		t.Fatalf("color column = %T, want string", v)
	}
	if !strings.HasPrefix(s, "#") || len(s) != 7 {
		t.Errorf("color = %q, want #RRGGBB format", s)
	}
}

func TestDataGenerator_GenerateForColumn_Slug(t *testing.T) {
	g := newGen(t)
	v := g.GenerateForColumn("slug", "VARCHAR", false, nil)
	if _, ok := v.(string); !ok {
		t.Errorf("slug column = %T, want string", v)
	}
}

func TestDataGenerator_GenerateForColumn_Token(t *testing.T) {
	g := newGen(t)
	v := g.GenerateForColumn("api_token", "VARCHAR", false, nil)
	if _, ok := v.(string); !ok {
		t.Errorf("api_token column = %T, want string", v)
	}
}

// ── DependencyGraph ───────────────────────────────────────────────────────────

func TestDependencyGraph_BuildInsertionOrder_Simple(t *testing.T) {
	g := NewDependencyGraph()
	g.AddTable(&TableInfo{Name: "users", Dependencies: []string{}})
	g.AddTable(&TableInfo{Name: "posts", Dependencies: []string{"users"}})
	g.AddTable(&TableInfo{Name: "comments", Dependencies: []string{"posts", "users"}})

	order, err := g.BuildInsertionOrder()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 3 {
		t.Fatalf("order = %d, want 3", len(order))
	}

	pos := func(name string) int {
		for i, n := range order {
			if n == name {
				return i
			}
		}
		return -1
	}

	if pos("users") >= pos("posts") {
		t.Errorf("users must come before posts: %v", order)
	}
	if pos("posts") >= pos("comments") {
		t.Errorf("posts must come before comments: %v", order)
	}
}

func TestDependencyGraph_BuildInsertionOrder_Circular(t *testing.T) {
	g := NewDependencyGraph()
	g.AddTable(&TableInfo{Name: "a", Dependencies: []string{"b"}})
	g.AddTable(&TableInfo{Name: "b", Dependencies: []string{"a"}})

	_, err := g.BuildInsertionOrder()
	if err == nil {
		t.Error("expected circular dependency error, got nil")
	}
}

func TestDependencyGraph_BuildInsertionOrder_SelfReference(t *testing.T) {
	g := NewDependencyGraph()
	g.AddTable(&TableInfo{Name: "categories", Dependencies: []string{"categories"}})

	order, err := g.BuildInsertionOrder()
	if err != nil {
		t.Fatalf("self-reference should not error: %v", err)
	}
	if len(order) != 1 {
		t.Errorf("order = %v, want [categories]", order)
	}
}

func TestDependencyGraph_GetOrder(t *testing.T) {
	g := NewDependencyGraph()
	g.AddTable(&TableInfo{Name: "users", Dependencies: []string{}})
	g.BuildInsertionOrder()
	if len(g.GetOrder()) != 1 {
		t.Errorf("GetOrder = %v, want [users]", g.GetOrder())
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func TestSplitColumnDefs(t *testing.T) {
	body := "id INT PRIMARY KEY, name VARCHAR(255), price DECIMAL(10, 2) NOT NULL"
	defs := splitColumnDefs(body)
	if len(defs) != 3 {
		t.Fatalf("splitColumnDefs = %d parts, want 3: %v", len(defs), defs)
	}
	if !strings.Contains(defs[2], "DECIMAL(10, 2)") {
		t.Errorf("third def missing DECIMAL(10, 2): %q", defs[2])
	}
	if !strings.Contains(defs[2], "NOT NULL") {
		t.Errorf("third def missing NOT NULL: %q", defs[2])
	}
}

func TestAdaptBatchSize(t *testing.T) {
	// Should not increase batch size
	if v := adaptBatchSize(10, 2); v != 10 {
		t.Errorf("adaptBatchSize(10, 2) = %d, want 10", v)
	}
	// Should clamp down for wide tables
	if v := adaptBatchSize(1000, 150); v != 50 {
		t.Errorf("adaptBatchSize(1000, 150) = %d, want 50", v)
	}
	// Default when zero
	if v := adaptBatchSize(0, 5); v != 100 {
		t.Errorf("adaptBatchSize(0, 5) = %d, want 100", v)
	}
}

func TestParseEnumValues(t *testing.T) {
	vals := parseEnumValues("ENUM('a','b','c')")
	if len(vals) != 3 || vals[0] != "a" || vals[1] != "b" || vals[2] != "c" {
		t.Errorf("parseEnumValues = %v, want [a b c]", vals)
	}

	vals2 := parseEnumValues(`ENUM("x","y")`)
	if len(vals2) != 2 || vals2[0] != "x" || vals2[1] != "y" {
		t.Errorf("parseEnumValues double quotes = %v, want [x y]", vals2)
	}

	if v := parseEnumValues("INT"); v != nil {
		t.Errorf("parseEnumValues(INT) = %v, want nil", v)
	}
}
