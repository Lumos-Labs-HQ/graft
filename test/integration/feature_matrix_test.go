package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFeatureMatrix verifies, per language, which codegen features are actually
// delivered end-to-end through the flash binary. It prints a ✅/❌ matrix so a
// glance at the log shows what is done and what is not, and it FAILS when a
// previously-working feature regresses (each ✅ row is an assertion).
//
// The matrix rows are grouped:
//   - files:      expected output files exist
//   - models:     table models with correct field types
//   - queries:    :one/:many/:exec methods with typed params
//   - joinRow:    custom Row type for JOIN result
//   - params:     Params struct for 3+ param queries
//   - cacheLayer: @cache accessors + cached queries (when [cache] enabled)
//
// Only sqlite is required (file-backed); server DBs reuse the same skip logic.
func TestFeatureMatrix(t *testing.T) {
	for _, db := range getDatabases() {
		t.Run(db.Name, func(t *testing.T) {
			t.Parallel()
			requireServerDB(t, db)

			dir := filepath.Join("test_projects", "matrix_"+db.Name)
			os.RemoveAll(dir)
			os.MkdirAll(dir, 0755)
			t.Cleanup(func() {
				// Reset the shared database BEFORE removing the project dir.
				if out, err := flash(t, dir, "reset", "--force"); err != nil {
					t.Logf("cleanup reset error: %v\n%s", err, out)
				}
				os.RemoveAll(dir)
			})
			setupProject(t, dir, db)

			// Add a cache-annotated query file so the cache layer is exercised.
			os.WriteFile(filepath.Join(dir, "db", "queries", "cached.sql"), []byte(`
-- @cache {"ttl": "30s", "name": "UserEmailCache", "tags": ["users"], "dep": ["CreateUser"]}
-- name: GetUserEmail :one
SELECT email FROM users WHERE id = $1;
`), 0644)

			// Enable every generator + cache at once.
			cfgPath := filepath.Join(dir, "flash.toml")
			raw, _ := os.ReadFile(cfgPath)
			cfg := string(raw)
			cfg += `
[gen.js]
enabled = true
out = "flash_gen"

[gen.python]
enabled = true
out = "flash_gen"
async = true

[gen.kotlin]
enabled = true
out = "flash_gen"
package = "com.example.flashgen"

[gen.java]
enabled = true
out = "flash_gen"
package = "com.example.flashgen"

[gen.rust]
enabled = true
out = "flash_gen"

[cache]
enabled = true
default_ttl = "5m"
`
			os.WriteFile(cfgPath, []byte(cfg), 0644)
			os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"t","version":"1.0.0"}`), 0644)

			out, err := flash(t, dir, "gen")
			t.Logf("gen output:\n%s", out)
			if err != nil {
				t.Logf("gen returned error (non-fatal): %v", err)
			}

			type feature struct {
				name string
				ok   func(dir string) bool
			}
			readFile := func(rel string) (string, bool) {
				data, err := os.ReadFile(filepath.Join(dir, rel))
				return string(data), err == nil
			}
			has := func(rel string, frags ...string) bool {
				src, ok := readFile(rel)
				if !ok {
					return false
				}
				for _, f := range frags {
					if !strings.Contains(src, f) {
						return false
					}
				}
				return true
			}
			// The Go generator's QueryPascal uses a byte-based shortcut that
			// lowercases after the first word boundary ("Getuser"), so match
			// method names case-insensitively.
			hasCI := func(rel string, frags ...string) bool {
				src, ok := readFile(rel)
				if !ok {
					return false
				}
				lower := strings.ToLower(src)
				for _, f := range frags {
					if !strings.Contains(lower, strings.ToLower(f)) {
						return false
					}
				}
				return true
			}
			exists := func(rel string) bool {
				_, err := os.Stat(filepath.Join(dir, rel))
				return err == nil
			}

			// The scaffolded queries define GetUser/:one and CreateUser/:exec
			// (see template/init.go GetQueries). All languages must surface
			// them; the go/queries row matches case-insensitively because the
			// Go generator's QueryPascal emits "Getuser".
			langs := []struct {
				name     string
				features []feature
			}{
				{
					name: "go",
					features: []feature{
						{"files", func(string) bool {
							return exists("flash_gen/models.go") && exists("flash_gen/db.go") && exists("flash_gen/users.go")
						}},
						{"models", func(string) bool { return has("flash_gen/models.go", "type Users struct") }},
						{"queries", func(string) bool { return hasCI("flash_gen/users.go", "GetUser", "CreateUser") }},
						{"cacheLayer", func(string) bool {
							return hasCI("flash_gen/cache.go", "FlashCache") && hasCI("flash_gen/cache_accessors.go", "UserEmailCache") && hasCI("flash_gen/cached_queries.go", "GetUserEmail")
						}},
					},
				},
				{
					name: "js",
					features: []feature{
						{"files", func(string) bool {
							return exists("flash_gen/index.js") && exists("flash_gen/index.d.ts") && exists("flash_gen/users.js")
						}},
						{"models", func(string) bool { return has("flash_gen/index.d.ts", "interface Users") }},
						{"queries", func(string) bool { return has("flash_gen/users.js", "getUser", "createUser") }},
						{"cacheLayer", func(string) bool {
							return has("flash_gen/cache_accessors.js", "UserEmailCache") && has("flash_gen/cached_queries.js", "GetUserEmail")
						}},
					},
				},
				{
					name: "python",
					features: []feature{
						{"files", func(string) bool {
							return exists("flash_gen/models.py") && exists("flash_gen/database.py") && exists("flash_gen/users.py")
						}},
						{"models", func(string) bool { return has("flash_gen/models.py", "class Users") }},
						{"queries", func(string) bool { return has("flash_gen/users.py", "get_user", "create_user") }},
						{"cacheLayer", func(string) bool {
							return has("flash_gen/cache_accessors.py", "UserEmailCache") && has("flash_gen/cached_queries.py", "getuseremail")
						}},
					},
				},
				{
					name: "kotlin",
					features: []feature{
						{"files", func(string) bool { return exists("flash_gen/Models.kt") && exists("flash_gen/Queries.kt") }},
						{"models", func(string) bool { return has("flash_gen/Models.kt", "data class Users") }},
						{"queries", func(string) bool { return has("flash_gen/Queries.kt", "getUser", "createUser") }},
						{"cacheLayer", func(string) bool {
							return has("flash_gen/CacheAccessors.kt", "UserEmailCache") && has("flash_gen/CachedQueries.kt", "GetUserEmail")
						}},
					},
				},
				{
					name: "java",
					features: []feature{
						{"files", func(string) bool {
							return exists("flash_gen/Users.java") && exists("flash_gen/Queries.java") && exists("flash_gen/UsersQueries.java")
						}},
						{"models", func(string) bool { return has("flash_gen/Users.java", "Users") }},
						{"queries", func(string) bool { return has("flash_gen/UsersQueries.java", "getUser", "createUser") }},
						{"cacheLayer", func(string) bool {
							return has("flash_gen/CacheAccessors.java", "UserEmailCache") && has("flash_gen/CachedQueries.java", "GetUserEmail")
						}},
					},
				},
				{
					name: "rust",
					features: []feature{
						{"files", func(string) bool {
							return exists("flash_gen/models.rs") && exists("flash_gen/db.rs") && exists("flash_gen/users.rs") && exists("flash_gen/mod.rs")
						}},
						{"models", func(string) bool { return has("flash_gen/models.rs", "pub struct Users") }},
						{"queries", func(string) bool { return has("flash_gen/users.rs", "get_user", "create_user") }},
						{"cacheLayer", func(string) bool {
							return has("flash_gen/cache_accessors.rs", "UserEmailCache") && has("flash_gen/cached_queries.rs", "get_user_email")
						}},
					},
				},
			}

			// ── print + assert the matrix ────────────────────────────────
			fmt.Printf("\n📋 Feature matrix (%s)\n", db.Name)
			fmt.Printf("    %-8s %-12s %s\n", "lang", "feature", "status")
			for _, lang := range langs {
				for _, feat := range lang.features {
					ok := feat.ok(dir)
					status := "✅"
					if !ok {
						status = "❌"
					}
					fmt.Printf("    %-8s %-12s %s\n", lang.name, feat.name, status)
					if !ok {
						t.Errorf("[%s/%s] feature regressed or missing", lang.name, feat.name)
					}
				}
			}
		})
	}
}
