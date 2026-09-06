package parser

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Lumos-Labs-HQ/flash/internal/config"
)

// ── Adversarial / malformed SQL that must not panic or hang ──────────────────

func TestAnalyzeQuery_AdversarialInputs(t *testing.T) {
	p := NewQueryParser(&config.Config{Database: config.Database{Provider: "postgresql"}})
	schema := &Schema{Tables: []*Table{
		{Name: "users", Columns: []*Column{
			{Name: "id", Type: "SERIAL"},
			{Name: "email", Type: "TEXT"},
			{Name: "settings", Type: "JSONB"},
		}},
	}}

	cases := []struct {
		name string
		sql  string
	}{
		{"empty sql", ""},
		{"only whitespace", "   "},
		{"unbalanced parens", "SELECT id FROM users WHERE (id = $1"},
		{"dollar sign alone", "SELECT id FROM users WHERE id = $"},
		{"dollar dollar", "SELECT $$body$$ FROM users WHERE id = $1"},
		{"huge param number", "SELECT id FROM users WHERE id = $999999999"},
		{"param zero", "SELECT id FROM users WHERE id = $0"},
		{"string with question mark", "SELECT id FROM users WHERE email = 'what?'"},
		{"nested subqueries", "SELECT id FROM users WHERE id IN (SELECT id FROM (SELECT id FROM users) x WHERE id = $1)"},
		{"case expression", "SELECT CASE WHEN id = $1 THEN 'a' ELSE 'b' END FROM users"},
		{"comment inside sql", "SELECT id FROM users WHERE id = $1 -- trailing comment"},
		{"backtick quoted table", "SELECT id FROM `users` WHERE id = $1"},
		{"double quoted table", `SELECT id FROM "users" WHERE id = $1`},
		{"semicolon heavy", "SELECT id FROM users WHERE id = $1;;;"},
		{"unicode content", "SELECT id FROM users WHERE email = $1 -- café ☕"},
		{"newline in sql", "SELECT id\nFROM users\nWHERE id = $1"},
		{"tab separated", "SELECT\tid\tFROM\tusers\tWHERE\tid\t=\t$1"},
		{"very long identifier", "SELECT " + strings.Repeat("a", 500) + " FROM users WHERE id = $1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &Query{Name: "Q", SQL: tc.sql}
			// Must not panic. Errors are acceptable for adversarial input.
			err := p.analyzeQuery(q, schema)
			if err != nil {
				t.Logf("analyzeQuery error (acceptable): %v", err)
			}
		})
	}
}

func TestInferParamName_AdversarialNoPanic(t *testing.T) {
	ti := NewTypeInferrer()
	inputs := []string{
		"",
		"$",
		"$$$$$",
		"?",
		"??????",
		"$0 $0 $0",
		"$-1",
		"$999999999999999999999",
		"IN ()",
		"IN ($1,)",
		"ANY($",
		"COALESCE(",
		"jsonb_set(",
		strings.Repeat("(", 200) + "$1" + strings.Repeat(")", 200),
	}
	for _, sql := range inputs {
		for idx := 1; idx <= 3; idx++ {
			_ = ti.InferParamName(sql, idx) // must not panic
		}
	}
}

func TestRewriteINListToANY_Adversarial(t *testing.T) {
	inputs := []string{
		"",
		"IN ()",
		"IN ($1)",
		"IN ($1,)",
		"IN ($1, $1, $1)",
		"id IN ($1) AND id IN ($2, $3)",
		"id IN ($3, $1, $2)",
		"id IN ($1, $2) AND email = $10",
		"col IN ($1, $2, $3) AND other IN ($4, $5)",
	}
	for _, sql := range inputs {
		_ = rewriteINListToANY(sql) // must not panic
	}
}

// ── Concurrency: the shared TypeInferrer and concurrent file parsing ────────

func TestParse_ConcurrentFilesNoRace(t *testing.T) {
	root := t.TempDir()
	schemaDir := filepath.Join(root, "schema")
	queriesDir := filepath.Join(root, "queries")
	os.MkdirAll(schemaDir, 0755)
	os.MkdirAll(queriesDir, 0755)
	writeSmokeSchema(t, schemaDir)

	for i := range 20 {
		content := "-- name: Q" + itoa(i) + " :one\nSELECT id, email FROM users WHERE id = $" + itoa((i%3)+1) + " AND email = $" + itoa((i%3)+2) + ";\n"
		writeQueryFile(t, queriesDir, "f"+itoa(i)+".sql", content)
	}

	cfg := &config.Config{
		SchemaDir:  schemaDir,
		SchemaPath: filepath.Join(schemaDir, "schema.sql"),
		Queries:    queriesDir,
	}
	schema, err := NewSchemaParser(cfg).Parse()
	if err != nil {
		t.Fatal(err)
	}
	// Run Parse repeatedly in parallel — shared inferrer + worker pool.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			p := NewQueryParser(cfg)
			if _, err := p.Parse(schema); err != nil {
				t.Errorf("Parse: %v", err)
			}
		})
	}
	wg.Wait()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// ── Keyspace-qualified table references (ScyllaDB mode) ─────────────────────

func TestAnalyzeQuery_KeyspaceQualifiedTable(t *testing.T) {
	p := NewQueryParser(&config.Config{Database: config.Database{Provider: "scylla"}})
	schema := &Schema{Tables: []*Table{
		{Name: "myks.users", Columns: []*Column{{Name: "id", Type: "UUID"}, {Name: "name", Type: "TEXT"}}},
	}}

	// Query uses plain table name, schema has ks-qualified → matchesTableName fallback.
	q := &Query{Name: "Q", SQL: "SELECT id FROM users WHERE id = $1"}
	if err := p.analyzeQuery(q, schema); err != nil {
		t.Fatalf("plain name against ks-qualified schema: %v", err)
	}
	if len(q.Params) != 1 || q.Params[0].Name != "id" {
		t.Errorf("params = %s", paramSummary(q))
	}

	// Query uses ks.tbl, schema has ks.tbl.
	q2 := &Query{Name: "Q2", SQL: "SELECT id FROM myks.users WHERE id = $1"}
	if err := p.analyzeQuery(q2, schema); err != nil {
		t.Fatalf("qualified name against ks-qualified schema: %v", err)
	}

	// Query uses other.users — different keyspace must NOT match myks.users.
	q3 := &Query{Name: "Q3", SQL: "SELECT id FROM other.users WHERE id = $1"}
	if err := p.analyzeQuery(q3, schema); err == nil {
		t.Error("different keyspace must not match")
	}
}

// ── Enum / CQL features ──────────────────────────────────────────────────────

func TestAnalyzeQuery_CQLCounterAndStatic(t *testing.T) {
	p := NewQueryParser(&config.Config{Database: config.Database{Provider: "scylla"}})
	schema := &Schema{
		Tables: []*Table{{Name: "events", Columns: []*Column{
			{Name: "id", Type: "UUID"},
			{Name: "count", Type: "COUNTER"},
			{Name: "name", Type: "TEXT", Nullable: true},
		}}},
	}
	q := &Query{Name: "Bump", SQL: "UPDATE events SET count = count + $1 WHERE id = $2"}
	if err := p.analyzeQuery(q, schema); err != nil {
		t.Fatalf("analyzeQuery: %v", err)
	}
	if len(q.Params) != 2 {
		t.Fatalf("params = %s", paramSummary(q))
	}
	// CQL provider → Nullable reflects schema nullability.
	for _, prm := range q.Params {
		if prm.Name == "name" && !prm.Nullable {
			t.Error("nullable column param should be Nullable under CQL provider")
		}
	}
}
