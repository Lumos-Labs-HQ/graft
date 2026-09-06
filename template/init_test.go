package template

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// ── NewProjectTemplate / DatabaseType constants ───────────────────────────────

func TestDatabaseTypeConstants(t *testing.T) {
	if SQLite != "sqlite" {
		t.Errorf("SQLite = %q, want sqlite", SQLite)
	}
	if PostgreSQL != "postgresql" {
		t.Errorf("PostgreSQL = %q, want postgresql", PostgreSQL)
	}
	if MySQL != "mysql" {
		t.Errorf("MySQL = %q, want mysql", MySQL)
	}
}

func TestValidateDatabaseType(t *testing.T) {
	cases := []struct {
		in   string
		want DatabaseType
	}{
		{"sqlite", SQLite},
		{"mysql", MySQL},
		{"postgresql", PostgreSQL},
		{"postgres", PostgreSQL},
		{"unknown", PostgreSQL}, // default
		{"", PostgreSQL},
	}
	for _, c := range cases {
		got := ValidateDatabaseType(c.in)
		if got != c.want {
			t.Errorf("ValidateDatabaseType(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ── GetFlashORMConfig ─────────────────────────────────────────────────────────

func TestGetFlashORMConfig_ValidTOML(t *testing.T) {
	for _, db := range []DatabaseType{PostgreSQL, MySQL, SQLite} {
		pt := NewProjectTemplate(db, false, false)
		cfg := pt.GetFlashORMConfig()
		var parsed map[string]any
		if err := toml.Unmarshal([]byte(cfg), &parsed); err != nil {
			t.Errorf("GetFlashORMConfig(%s) invalid TOML: %v\n%s", db, err, cfg)
		}
	}
}

func TestGetFlashORMConfig_CorrectProvider(t *testing.T) {
	cases := []struct {
		db   DatabaseType
		want string
	}{
		{PostgreSQL, "postgresql"},
		{MySQL, "mysql"},
		{SQLite, "sqlite"},
	}
	for _, c := range cases {
		pt := NewProjectTemplate(c.db, false, false)
		cfg := pt.GetFlashORMConfig()
		if !strings.Contains(cfg, c.want) {
			t.Errorf("GetFlashORMConfig(%s) missing provider %q", c.db, c.want)
		}
	}
}

func TestGetFlashORMConfig_GoGen(t *testing.T) {
	pt := NewProjectTemplate(PostgreSQL, false, false)
	cfg := pt.GetFlashORMConfig()
	if !strings.Contains(cfg, `[gen.go]`) {
		t.Errorf("Go project config missing go gen section: %s", cfg)
	}
}

func TestGetFlashORMConfig_JSGen(t *testing.T) {
	pt := NewProjectTemplate(PostgreSQL, true, false)
	cfg := pt.GetFlashORMConfig()
	if !strings.Contains(cfg, `[gen.js]`) {
		t.Errorf("Node project config missing js gen section: %s", cfg)
	}
}

func TestGetFlashORMConfig_PythonGen(t *testing.T) {
	pt := NewProjectTemplate(PostgreSQL, false, true)
	cfg := pt.GetFlashORMConfig()
	if !strings.Contains(cfg, `[gen.python]`) {
		t.Errorf("Python project config missing python gen section: %s", cfg)
	}
}

// ── GetSchema ─────────────────────────────────────────────────────────────────

func TestGetSchema_ContainsUsersTable(t *testing.T) {
	for _, db := range []DatabaseType{PostgreSQL, MySQL, SQLite} {
		pt := NewProjectTemplate(db, false, false)
		schema := pt.GetSchema()
		if !strings.Contains(schema, "CREATE TABLE users") {
			t.Errorf("GetSchema(%s) missing CREATE TABLE users", db)
		}
		for _, col := range []string{"id", "name", "email", "created_at"} {
			if !strings.Contains(schema, col) {
				t.Errorf("GetSchema(%s) missing column %q", db, col)
			}
		}
	}
}

func TestGetSchema_MySQL_HasOnUpdate(t *testing.T) {
	pt := NewProjectTemplate(MySQL, false, false)
	schema := pt.GetSchema()
	if !strings.Contains(schema, "ON UPDATE CURRENT_TIMESTAMP") {
		t.Errorf("MySQL schema missing ON UPDATE CURRENT_TIMESTAMP: %s", schema)
	}
}

func TestGetSchema_PostgreSQL_NoOnUpdate(t *testing.T) {
	pt := NewProjectTemplate(PostgreSQL, false, false)
	schema := pt.GetSchema()
	if strings.Contains(schema, "ON UPDATE") {
		t.Errorf("PostgreSQL schema should not have ON UPDATE: %s", schema)
	}
}

func TestGetSchema_SQLite_PrimaryKey(t *testing.T) {
	pt := NewProjectTemplate(SQLite, false, false)
	schema := pt.GetSchema()
	if !strings.Contains(schema, "AUTOINCREMENT") {
		t.Errorf("SQLite schema missing AUTOINCREMENT: %s", schema)
	}
}

// ── GetQueries ────────────────────────────────────────────────────────────────

func TestGetQueries_ContainsGetUser(t *testing.T) {
	for _, db := range []DatabaseType{PostgreSQL, MySQL, SQLite} {
		pt := NewProjectTemplate(db, false, false)
		q := pt.GetQueries()
		if !strings.Contains(q, "GetUser") {
			t.Errorf("GetQueries(%s) missing GetUser", db)
		}
		if !strings.Contains(q, "CreateUser") {
			t.Errorf("GetQueries(%s) missing CreateUser", db)
		}
	}
}

func TestGetQueries_PostgreSQL_DollarParams(t *testing.T) {
	pt := NewProjectTemplate(PostgreSQL, false, false)
	q := pt.GetQueries()
	if !strings.Contains(q, "$1") {
		t.Errorf("PostgreSQL queries missing $1 param: %s", q)
	}
	if !strings.Contains(q, "RETURNING") {
		t.Errorf("PostgreSQL queries missing RETURNING clause: %s", q)
	}
}

func TestGetQueries_MySQL_QuestionMark(t *testing.T) {
	pt := NewProjectTemplate(MySQL, false, false)
	q := pt.GetQueries()
	if !strings.Contains(q, "?") {
		t.Errorf("MySQL queries missing ? param: %s", q)
	}
	if strings.Contains(q, "RETURNING") {
		t.Errorf("MySQL queries should not have RETURNING: %s", q)
	}
}

func TestGetQueries_SQLite_QuestionMark(t *testing.T) {
	pt := NewProjectTemplate(SQLite, false, false)
	q := pt.GetQueries()
	if !strings.Contains(q, "?") {
		t.Errorf("SQLite queries missing ? param: %s", q)
	}
}

// ── GetEnvTemplate ────────────────────────────────────────────────────────────

func TestGetEnvTemplate_ContainsDatabaseURL(t *testing.T) {
	for _, db := range []DatabaseType{PostgreSQL, MySQL, SQLite} {
		pt := NewProjectTemplate(db, false, false)
		env := pt.GetEnvTemplate()
		if !strings.Contains(env, "DATABASE_URL=") {
			t.Errorf("GetEnvTemplate(%s) missing DATABASE_URL: %s", db, env)
		}
	}
}

func TestGetEnvTemplate_CorrectScheme(t *testing.T) {
	cases := []struct {
		db     DatabaseType
		scheme string
	}{
		{PostgreSQL, "postgres://"},
		{MySQL, "mysql://"},
		{SQLite, "sqlite://"},
	}
	for _, c := range cases {
		pt := NewProjectTemplate(c.db, false, false)
		env := pt.GetEnvTemplate()
		if !strings.Contains(env, c.scheme) {
			t.Errorf("GetEnvTemplate(%s) missing scheme %q: %s", c.db, c.scheme, env)
		}
	}
}

// ── GetDirectoryStructure ─────────────────────────────────────────────────────

func TestGetDirectoryStructure(t *testing.T) {
	pt := NewProjectTemplate(PostgreSQL, false, false)
	dirs := pt.GetDirectoryStructure()
	if len(dirs) == 0 {
		t.Error("GetDirectoryStructure should return at least one directory")
	}
	dirSet := map[string]bool{}
	for _, d := range dirs {
		dirSet[d] = true
	}
	for _, want := range []string{"db/schema", "db/queries"} {
		if !dirSet[want] {
			t.Errorf("GetDirectoryStructure missing %q: %v", want, dirs)
		}
	}
}
