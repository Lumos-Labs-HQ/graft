package template

import (
	"strings"
	"testing"
)

// allDBTypes are every database the guide must render cleanly for.
var allDBTypes = []DatabaseType{PostgreSQL, MySQL, SQLite, ClickHouse, ScyllaDB}

// ── GetAgentGuide ─────────────────────────────────────────────────────────────

// The guide is authored with @@TOKEN@@ placeholders and a § backtick sentinel,
// both of which must be fully substituted before the file is written. A leaked
// token or sentinel is the single most likely rendering bug, so assert against
// it for every database.
func TestGetAgentGuide_NoUnreplacedTokens(t *testing.T) {
	for _, db := range allDBTypes {
		pt := NewProjectTemplate(db, false, false)
		guide := pt.GetAgentGuide()
		if strings.Contains(guide, "@@") {
			t.Errorf("GetAgentGuide(%s) has an unreplaced @@TOKEN@@:\n%s", db, firstLineWith(guide, "@@"))
		}
		if strings.Contains(guide, "§") {
			t.Errorf("GetAgentGuide(%s) has a leftover § backtick sentinel:\n%s", db, firstLineWith(guide, "§"))
		}
	}
}

func TestGetAgentGuide_Title(t *testing.T) {
	pt := NewProjectTemplate(PostgreSQL, false, false)
	guide := pt.GetAgentGuide()
	if !strings.HasPrefix(guide, "# FLASH.md — FlashORM Agent Guide") {
		t.Errorf("GetAgentGuide missing title header, got first line: %q", firstLine(guide))
	}
}

// The guide is DB-tailored: it must name the project's provider and use the
// right query-parameter style ($1 for Postgres, ? for the rest).
func TestGetAgentGuide_ProviderAndParam(t *testing.T) {
	for _, db := range allDBTypes {
		cfg := dbConfigs[db]
		pt := NewProjectTemplate(db, false, false)
		guide := pt.GetAgentGuide()
		if !strings.Contains(guide, cfg.provider) {
			t.Errorf("GetAgentGuide(%s) does not mention provider %q", db, cfg.provider)
		}
		if !strings.Contains(guide, "`"+cfg.queryParam+"`") {
			t.Errorf("GetAgentGuide(%s) does not show query param style %q", db, cfg.queryParam)
		}
	}
}

func TestGetAgentGuide_PostgreSQLUsesDollarParam(t *testing.T) {
	pt := NewProjectTemplate(PostgreSQL, false, false)
	guide := pt.GetAgentGuide()
	if !strings.Contains(guide, "$1, $2") {
		t.Errorf("Postgres guide should describe $1, $2 numbered params")
	}
}

// The guide embeds the exact schema and queries scaffolded on disk, so an agent
// reads a description that matches the real files.
func TestGetAgentGuide_EmbedsSchemaAndQueries(t *testing.T) {
	for _, db := range allDBTypes {
		pt := NewProjectTemplate(db, false, false)
		guide := pt.GetAgentGuide()
		// The guide embeds GetSchema()/GetQueries() verbatim. Assert against the
		// tokens common to every provider (Scylla uses CQL: CREATE TABLE
		// myapp.users, GetUserByID) rather than Postgres-specific strings.
		if !strings.Contains(guide, "CREATE TABLE") || !strings.Contains(guide, "users") {
			t.Errorf("GetAgentGuide(%s) does not embed the scaffolded schema", db)
		}
		if !strings.Contains(guide, "-- name:") || !strings.Contains(guide, "CreateUser") {
			t.Errorf("GetAgentGuide(%s) does not embed the scaffolded queries", db)
		}
	}
}

// Every major reference section must be present — the whole point is that one
// file is enough to drive Flash.
func TestGetAgentGuide_HasAllSections(t *testing.T) {
	pt := NewProjectTemplate(PostgreSQL, false, false)
	guide := pt.GetAgentGuide()
	sections := []string{
		"## 1. What FlashORM is",
		"## 2. Where generated code lives",
		"## 3. Project layout",
		"## 4. Everyday workflow",
		"## 5. CLI command reference",
		"## 6. flash.toml reference",
		"## 7. Database connection",
		"## 8. Writing schema",
		"## 9. Writing queries",
		"## 10. Supported databases",
		"## 11. Annotations",
		"## 12. Typed JSON columns",
		"## 13. Query caching",
		"## 14. Code generation",
		"## 15. FlashORM Studio",
		"## 16. Filing bugs",
		"## 17. Golden rules",
	}
	for _, s := range sections {
		if !strings.Contains(guide, s) {
			t.Errorf("GetAgentGuide missing section %q", s)
		}
	}
}

// The guide documents the query grammar and the caching/annotation surface that
// an agent needs to actually write correct Flash SQL.
func TestGetAgentGuide_DocumentsGrammarAndCache(t *testing.T) {
	pt := NewProjectTemplate(PostgreSQL, false, false)
	guide := pt.GetAgentGuide()
	needles := []string{
		"-- name:",    // query grammar
		":one",        // result modes
		":many",       //
		":exec",       //
		":execresult", //
		"-- @cache",   // caching annotation
		"-- @required:",
		"-- @json",
		"default_ttl",      // config knob
		"flash issues",     // ties into the reporting command
		"macros = false",   // Rust runtime-checked mode
		"macros = true",    // Rust compile-time macro mode
		"sqlx::query_as",   // Rust runtime API
		"cargo check",      // Rust compile-time verification workflow
		"FlashInitMigrate", // binary-independent startup migration helper
	}
	for _, n := range needles {
		if !strings.Contains(guide, n) {
			t.Errorf("GetAgentGuide missing expected content %q", n)
		}
	}
}

func TestGetAgentGuide_NamesDetectedLanguageAndProvidesExample(t *testing.T) {
	cases := []struct {
		name string
		pt   *ProjectTemplate
		lang string
		code string
	}{
		{"go", NewProjectTemplateExt(PostgreSQL, false, false, false, false, false), "Go", "Newq"},
		{"node", NewProjectTemplateExt(PostgreSQL, true, false, false, false, false), "JavaScript/TypeScript", "Newq"},
		{"python", NewProjectTemplateExt(PostgreSQL, false, true, false, false, false), "Python", "asyncpg"},
		{"kotlin", NewProjectTemplateExt(PostgreSQL, false, false, true, false, false), "Kotlin", "getUser"},
		{"java", NewProjectTemplateExt(PostgreSQL, false, false, false, true, false), "Java", "Queries.newq"},
		{"rust", NewProjectTemplateExt(PostgreSQL, false, false, false, false, true), "Rust", "sqlx::PgPool"},
	}
	for _, tc := range cases {
		guide := tc.pt.GetAgentGuide()
		if !strings.Contains(guide, "This project was initialized for **"+tc.lang+"**") {
			t.Errorf("%s guide does not identify detected language", tc.name)
		}
		if !strings.Contains(guide, tc.code) {
			t.Errorf("%s guide does not contain language-specific example %q", tc.name, tc.code)
		}
		if strings.Contains(guide, "@@") || strings.Contains(guide, "§") {
			t.Errorf("%s guide has unreplaced template markers", tc.name)
		}
	}
}

// The guide must reference each provider's real connection-string example so the
// env section is actionable.
func TestGetAgentGuide_ContainsEnvExample(t *testing.T) {
	for _, db := range allDBTypes {
		cfg := dbConfigs[db]
		pt := NewProjectTemplate(db, false, false)
		guide := pt.GetAgentGuide()
		if !strings.Contains(guide, cfg.envExample) {
			t.Errorf("GetAgentGuide(%s) missing env example %q", db, cfg.envExample)
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func firstLine(s string) string {
	if before, _, ok := strings.Cut(s, "\n"); ok {
		return before
	}
	return s
}

func firstLineWith(s, substr string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	return ""
}
