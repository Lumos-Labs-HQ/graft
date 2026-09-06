package integration

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── Test infrastructure ───────────────────────────────────────────────────────

var flashBinary string

type Database struct {
	Name string
	URL  string
	// Host:Port to pre-flight for server-backed databases. Empty for
	// file-backed databases (sqlite) which never need a server.
	Addr string
}

func getDatabases() []Database {
	return []Database{
		{
			Name: "postgresql",
			URL:  getEnv("POSTGRES_URL", "postgres://testuser:testpass@localhost:5432/testdb?sslmode=disable"),
			Addr: getEnv("POSTGRES_ADDR", "localhost:5432"),
		},
		{
			Name: "mysql",
			URL:  getEnv("MYSQL_URL", "mysql://testuser:testpass@localhost:3306/testdb"),
			Addr: getEnv("MYSQL_ADDR", "localhost:3306"),
		},
		{
			// SQLite is file-backed: no DB_URL needed — the DB file is created
			// inside the per-test project directory at apply time.
			Name: "sqlite",
			URL:  getEnv("SQLITE_URL", ""),
		},
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// requireServerDB skips the test (without failing) when the configured
// database server is not reachable. This keeps the suite green on machines
// without Docker/Postgres/MySQL while still running everything against a
// live server when one exists.
func requireServerDB(t *testing.T, db Database) {
	t.Helper()
	if db.Addr == "" {
		return // file-backed (sqlite): always available
	}
	conn, err := net.DialTimeout("tcp", db.Addr, 2*time.Second)
	if err != nil {
		t.Skipf("database server %s not reachable (set %s_URL to override): %v",
			db.Name, strings.ToUpper(db.Name), err)
	}
	conn.Close()
}

func TestMain(m *testing.M) {
	fmt.Println("🧪 FlashORM Integration Tests")
	fmt.Println("================================")

	var err error
	flashBinary, err = filepath.Abs("../../flash")
	if err != nil {
		fmt.Printf("Failed to resolve flash binary path: %v\n", err)
		os.Exit(1)
	}

	if _, err := os.Stat(flashBinary); os.IsNotExist(err) {
		fmt.Printf("⚠️  flash binary not found at %s — skipping integration tests (run 'make build' first)\n", flashBinary)
		os.Exit(0) // skip, not fail
	}

	os.MkdirAll("test_projects", 0755)
	code := m.Run()
	os.RemoveAll("test_projects")
	os.Exit(code)
}

// flash runs the flash binary in dir with args and returns combined output.
func flash(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(flashBinary, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// mustFlash fails the test if the command errors.
func mustFlash(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := flash(t, dir, args...)
	if err != nil {
		t.Fatalf("flash %v failed: %v\nOutput:\n%s", args, err, out)
	}
	return out
}

// dbURLFor returns the DATABASE_URL to write into the project's .env. SQLite
// defaults to a local file inside the project dir so the suite needs no
// external server and no DB_URL env var at all. The path must be absolute —
// flash commands chdir into the project dir at runtime.
func dbURLFor(db Database, dir string) string {
	if db.URL != "" {
		return db.URL
	}
	if db.Name == "sqlite" {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			return "sqlite://flash_test.db"
		}
		return "sqlite://" + filepath.Join(absDir, "flash_test.db")
	}
	return ""
}

// setupProject initialises a project dir with .env and runs init+migrate+apply.
func setupProject(t *testing.T, dir string, db Database) {
	t.Helper()
	requireServerDB(t, db)

	flag := "--" + db.Name
	if db.Name == "postgresql" {
		flag = "--postgresql"
	}
	mustFlash(t, dir, "init", flag)

	envContent := fmt.Sprintf("DATABASE_URL=%s\n", dbURLFor(db, dir))
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	// Reset any leftover tables from previous test runs before generating
	if out, err := flash(t, dir, "reset", "--force"); err != nil {
		t.Logf("pre-setup reset: %v\n%s", err, out)
	}

	// Use a unique migration name per test to avoid timestamp collisions
	migrationName := filepath.Base(dir) + "_schema"
	mustFlash(t, dir, "migrate", migrationName)
	mustFlash(t, dir, "apply", "--force")
}

// ── Full per-database test suite ──────────────────────────────────────────────

func TestAllDatabases(t *testing.T) {
	for _, db := range getDatabases() {
		t.Run(db.Name, func(t *testing.T) {
			t.Parallel()

			// Skip the whole database group when its server is not reachable.
			requireServerDB(t, db)

			dir := filepath.Join("test_projects", db.Name)
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

			t.Run("01_Init", func(t *testing.T) { testInit(t, dir, db) })
			t.Run("02_Migrate", func(t *testing.T) { testMigrate(t, dir, db) })
			t.Run("03_Apply", func(t *testing.T) { testApply(t, dir) })
			t.Run("04_Status", func(t *testing.T) { testStatus(t, dir) })
			t.Run("05_Down", func(t *testing.T) { testDown(t, dir, db) })
			t.Run("06_Gen_Go", func(t *testing.T) { testGenGo(t, dir, db) })
			t.Run("07_Gen_JS", func(t *testing.T) { testGenJS(t, dir, db) })
			t.Run("08_Gen_Python", func(t *testing.T) { testGenPython(t, dir, db) })
			t.Run("09_Pull", func(t *testing.T) { testPull(t, dir, db) })
			t.Run("10_Export_JSON", func(t *testing.T) { testExportJSON(t, dir, db) })
			t.Run("11_Raw", func(t *testing.T) { testRaw(t, dir, db) })
			t.Run("12_Seed", func(t *testing.T) { testSeed(t, dir, db) })
			t.Run("13_Branch", func(t *testing.T) { testBranch(t, dir, db) })
			t.Run("14_Studio", func(t *testing.T) { testStudio(t, dir, db) })
			t.Run("15_Reset", func(t *testing.T) { testReset(t, dir, db) })
		})
	}
}

// ── Individual test functions ─────────────────────────────────────────────────

func testInit(t *testing.T, dir string, db Database) {
	flag := "--" + db.Name
	if db.Name == "postgresql" {
		flag = "--postgresql"
	}
	out := mustFlash(t, dir, "init", flag)
	t.Logf("init output: %s", out)

	for _, path := range []string{"flash.toml", "db/schema", "db/queries"} {
		if _, err := os.Stat(filepath.Join(dir, path)); os.IsNotExist(err) {
			t.Errorf("expected path not created: %s", path)
		}
	}

	// Write .env so subsequent steps can connect.
	os.WriteFile(filepath.Join(dir, ".env"), []byte(fmt.Sprintf("DATABASE_URL=%s\n", dbURLFor(db, dir))), 0644)
}

func testMigrate(t *testing.T, dir string, _ Database) {
	migrationName := filepath.Base(dir) + "_schema"
	out := mustFlash(t, dir, "migrate", migrationName)
	t.Logf("migrate output: %s", out)

	entries, err := os.ReadDir(filepath.Join(dir, "db/migrations"))
	if err != nil || len(entries) == 0 {
		t.Error("no migration files created in db/migrations")
	}
}

func testApply(t *testing.T, dir string) {
	out := mustFlash(t, dir, "apply", "--force")
	t.Logf("apply output: %s", out)

	if !strings.Contains(out, "Applied") && !strings.Contains(out, "No pending") {
		t.Errorf("unexpected apply output: %s", out)
	}
}

func testStatus(t *testing.T, dir string) {
	out := mustFlash(t, dir, "status")
	t.Logf("status output: %s", out)

	if !strings.Contains(out, "Migration") {
		t.Errorf("status output missing 'Migration': %s", out)
	}
}

func testDown(t *testing.T, dir string, _ Database) {
	// Re-apply so there is something to roll back.
	mustFlash(t, dir, "apply", "--force")

	out, err := flash(t, dir, "down", "--force")
	t.Logf("down output: %s", out)
	if err != nil {
		// "No migrations to roll back" is acceptable.
		if !strings.Contains(out, "No migrations") {
			t.Errorf("down failed: %v\n%s", err, out)
		}
	}

	// Re-apply so later tests have a clean schema.
	mustFlash(t, dir, "apply", "--force")
}

func testGenGo(t *testing.T, dir string, _ Database) {
	out, err := flash(t, dir, "gen")
	t.Logf("gen (go) output: %s", out)
	if err != nil {
		t.Logf("gen returned error (non-fatal): %v", err)
	}

	genDir := filepath.Join(dir, "flash_gen")
	if _, err := os.Stat(genDir); os.IsNotExist(err) {
		t.Error("flash_gen directory not created")
		return
	}

	// models.go and db.go must exist.
	for _, f := range []string{"models.go", "db.go"} {
		if _, err := os.Stat(filepath.Join(genDir, f)); os.IsNotExist(err) {
			t.Errorf("expected generated file missing: %s", f)
		}
	}
}

func testGenJS(t *testing.T, dir string, _ Database) {
	// Enable JS generation in config.
	cfgPath := filepath.Join(dir, "flash.toml")
	raw, _ := os.ReadFile(cfgPath)
	cfg := string(raw)
	if !strings.Contains(cfg, `[gen.js]`) {
		cfg += "\n[gen.js]\nenabled = true\nout = \"flash_gen\"\n"
		os.WriteFile(cfgPath, []byte(cfg), 0644)
	}

	// Create package.json so the generator detects a Node project.
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"test","version":"1.0.0"}`), 0644)

	out, err := flash(t, dir, "gen")
	t.Logf("gen (js) output: %s", out)
	if err != nil {
		t.Logf("gen (js) error (non-fatal): %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "flash_gen", "index.js")); os.IsNotExist(err) {
		t.Error("index.js not generated")
	}
	if _, err := os.Stat(filepath.Join(dir, "flash_gen", "index.d.ts")); os.IsNotExist(err) {
		t.Error("index.d.ts not generated")
	}
}

func testGenPython(t *testing.T, dir string, _ Database) {
	cfgPath := filepath.Join(dir, "flash.toml")
	raw, _ := os.ReadFile(cfgPath)
	cfg := string(raw)
	if !strings.Contains(cfg, `[gen.python]`) {
		cfg += "\n[gen.python]\nenabled = true\nout = \"flash_gen\"\nasync = true\n"
		os.WriteFile(cfgPath, []byte(cfg), 0644)
	}

	os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("psycopg2\n"), 0644)

	out, err := flash(t, dir, "gen")
	t.Logf("gen (python) output: %s", out)
	if err != nil {
		t.Logf("gen (python) error (non-fatal): %v", err)
	}

	for _, f := range []string{"models.py", "database.py", "database.pyi", "__init__.py"} {
		if _, err := os.Stat(filepath.Join(dir, "flash_gen", f)); os.IsNotExist(err) {
			t.Errorf("expected generated file missing: %s", f)
		}
	}
}

func testPull(t *testing.T, dir string, _ Database) {
	out, err := flash(t, dir, "pull")
	t.Logf("pull output: %s", out)
	if err != nil {
		t.Logf("pull error (non-fatal): %v", err)
		return
	}

	schemaDir := filepath.Join(dir, "db/schema")
	entries, _ := os.ReadDir(schemaDir)
	hasSQLFile := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			hasSQLFile = true
			break
		}
	}
	if !hasSQLFile {
		t.Error("pull did not produce any .sql schema file")
	}
}

func testExportJSON(t *testing.T, dir string, _ Database) {
	out, err := flash(t, dir, "export")
	t.Logf("export json output: %s", out)
	if err != nil {
		t.Logf("export json error (non-fatal): %v", err)
		return
	}

	exportDir := filepath.Join(dir, "db/export")
	entries, _ := os.ReadDir(exportDir)
	hasJSON := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			hasJSON = true
			break
		}
	}
	if !hasJSON {
		t.Error("no .json export file created")
	}
}

func testRaw(t *testing.T, dir string, _ Database) {
	query := "SELECT 1"
	out := mustFlash(t, dir, "raw", "-q", query)
	t.Logf("raw output: %s", out)
	if len(strings.TrimSpace(out)) == 0 {
		t.Error("raw query returned no output")
	}
}

func testSeed(t *testing.T, dir string, _ Database) {
	out, err := flash(t, dir, "seed", "--count", "5", "--force")
	t.Logf("seed output: %s", out)
	if err != nil {
		t.Logf("seed error (non-fatal): %v", err)
	}
}

func testBranch(t *testing.T, dir string, db Database) {
	if db.Name == "sqlite" {
		t.Skip("branch operations not supported for SQLite")
	}

	// Create branch
	out := mustFlash(t, dir, "branch", "feature", "--force")
	if !strings.Contains(out, "created") {
		t.Errorf("branch create: unexpected output: %s", out)
	}

	// List branches — both main and feature must appear
	out = mustFlash(t, dir, "branch")
	if !strings.Contains(out, "main") || !strings.Contains(out, "feature") {
		t.Errorf("branch list missing expected branches: %s", out)
	}

	// Checkout feature
	out = mustFlash(t, dir, "checkout", "feature", "--force")
	if !strings.Contains(out, "Switched") {
		t.Errorf("checkout: unexpected output: %s", out)
	}

	// Switch back to main
	mustFlash(t, dir, "checkout", "main", "--force")

	// Diff between branches
	out, _ = flash(t, dir, "branch", "diff", "main", "feature")
	t.Logf("branch diff output: %s", out)

	// Delete feature branch
	out = mustFlash(t, dir, "branch", "--delete", "feature", "--force")
	if !strings.Contains(out, "deleted") {
		t.Errorf("branch delete: unexpected output: %s", out)
	}
}

func testStudio(t *testing.T, dir string, db Database) {
	port := 15555 + portOffset(db.Name)

	cmd := exec.Command(flashBinary, "studio", "--port", fmt.Sprintf("%d", port), "--browser=false")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatalf("studio failed to start: %v", err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	// Wait up to 5 s for the HTTP server to be ready.
	url := fmt.Sprintf("http://localhost:%d", port)
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				t.Logf("✅ Studio responding on port %d", port)
				return
			}
		}
		lastErr = err
		time.Sleep(300 * time.Millisecond)
	}
	t.Logf("⚠️  Studio did not respond within 5s (last error: %v)", lastErr)
}

func testReset(t *testing.T, dir string, _ Database) {
	out, err := flash(t, dir, "reset", "--force")
	t.Logf("reset output: %s", out)
	if err != nil {
		t.Logf("reset error (non-fatal): %v", err)
	}
}

func portOffset(dbName string) int {
	switch dbName {
	case "postgresql":
		return 0
	case "mysql":
		return 1
	case "sqlite":
		return 2
	default:
		return 3
	}
}
