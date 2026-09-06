package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCodegen verifies that all three code generators produce the expected files
// for every supported database provider.
func TestCodegen(t *testing.T) {
	for _, db := range getDatabases() {
		t.Run(db.Name, func(t *testing.T) {
			t.Parallel()

			// Skip cleanly when the database server is not reachable.
			requireServerDB(t, db)

			dir := filepath.Join("test_projects", "codegen_"+db.Name)
			os.RemoveAll(dir)
			os.MkdirAll(dir, 0755)
			t.Cleanup(func() {
				// Reset the shared database BEFORE removing the project directory,
				// because flash needs to chdir into it to read config.
				if out, err := flash(t, dir, "reset", "--force"); err != nil {
					t.Logf("cleanup reset error: %v\n%s", err, out)
				}
				os.RemoveAll(dir)
			})

			setupProject(t, dir, db)

			t.Run("Go", func(t *testing.T) {
				enableGen(t, dir, "go", "\n[gen.go]\nenabled = true\nout = \"flash_gen\"\ndriver = \"database/sql\"\n")
				out, err := flash(t, dir, "gen")
				t.Logf("gen go: %s", out)
				if err != nil {
					t.Logf("gen go error (non-fatal): %v", err)
				}
				for _, f := range []string{"models.go", "db.go"} {
					if _, err := os.Stat(filepath.Join(dir, "flash_gen", f)); os.IsNotExist(err) {
						t.Errorf("missing %s", f)
					}
				}
			})

			t.Run("JavaScript", func(t *testing.T) {
				enableGen(t, dir, "js", "\n[gen.js]\nenabled = true\nout = \"flash_gen\"\ndriver = \"pg\"\n")
				os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"t","version":"1.0.0"}`), 0644)

				out, err := flash(t, dir, "gen")
				t.Logf("gen js: %s", out)
				if err != nil {
					t.Logf("gen js error (non-fatal): %v", err)
				}
				for _, f := range []string{"index.js", "index.d.ts", "users.js", "migrations.js"} {
					if _, err := os.Stat(filepath.Join(dir, "flash_gen", f)); os.IsNotExist(err) {
						t.Errorf("missing %s", f)
					}
				}
			})

			t.Run("Python", func(t *testing.T) {
				enableGen(t, dir, "python", "\n[gen.python]\nenabled = true\nout = \"flash_gen\"\nasync = true\ndriver = \"asyncpg\"\n")
				os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("asyncpg\n"), 0644)

				out, err := flash(t, dir, "gen")
				t.Logf("gen python: %s", out)
				if err != nil {
					t.Logf("gen python error (non-fatal): %v", err)
				}
				for _, f := range []string{"models.py", "database.py", "database.pyi", "__init__.py", "users.py", "migrations.py"} {
					if _, err := os.Stat(filepath.Join(dir, "flash_gen", f)); os.IsNotExist(err) {
						t.Errorf("missing %s", f)
					}
				}
			})

			t.Run("Kotlin", func(t *testing.T) {
				enableGen(t, dir, "kotlin", "\n[gen.kotlin]\nenabled = true\nout = \"flash_gen\"\npackage = \"com.example.flashgen\"\ndriver = \"jdbc\"\n")

				out, err := flash(t, dir, "gen")
				t.Logf("gen kotlin: %s", out)
				if err != nil {
					t.Logf("gen kotlin error (non-fatal): %v", err)
				}
				for _, f := range []string{"Models.kt", "Queries.kt", "FlashMigrations.kt"} {
					if _, err := os.Stat(filepath.Join(dir, "flash_gen", f)); os.IsNotExist(err) {
						t.Errorf("missing %s", f)
					}
				}
			})

			t.Run("Java", func(t *testing.T) {
				enableGen(t, dir, "java", "\n[gen.java]\nenabled = true\nout = \"flash_gen\"\npackage = \"com.example.flashgen\"\ndriver = \"jdbc\"\n")

				out, err := flash(t, dir, "gen")
				t.Logf("gen java: %s", out)
				if err != nil {
					t.Logf("gen java error (non-fatal): %v", err)
				}
				for _, f := range []string{"Users.java", "Queries.java", "UsersQueries.java", "FlashMigrations.java"} {
					if _, err := os.Stat(filepath.Join(dir, "flash_gen", f)); os.IsNotExist(err) {
						t.Errorf("missing %s", f)
					}
				}
			})

			t.Run("Rust", func(t *testing.T) {
				enableGen(t, dir, "rust", "\n[gen.rust]\nenabled = true\nout = \"flash_gen\"\ndriver = \"sqlx\"\n")

				out, err := flash(t, dir, "gen")
				t.Logf("gen rust: %s", out)
				if err != nil {
					t.Logf("gen rust error (non-fatal): %v", err)
				}
				for _, f := range []string{"mod.rs", "models.rs", "db.rs", "users.rs", "migrations.rs"} {
					if _, err := os.Stat(filepath.Join(dir, "flash_gen", f)); os.IsNotExist(err) {
						t.Errorf("missing %s", f)
					}
				}
			})
		})
	}
}

// enableGen appends a [gen.<lang>] section to flash.toml if not present.
func enableGen(t *testing.T, dir, lang, section string) {
	t.Helper()
	cfgPath := filepath.Join(dir, "flash.toml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read flash.toml: %v", err)
	}
	cfg := string(raw)
	if strings.Contains(cfg, "[gen."+lang+"]") {
		return
	}
	if err := os.WriteFile(cfgPath, []byte(cfg+section), 0644); err != nil {
		t.Fatalf("write flash.toml: %v", err)
	}
}
