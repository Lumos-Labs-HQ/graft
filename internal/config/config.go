package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// ConfigFile is the path to the config file, set by the cmd package from --config flag.
var ConfigFile string

var (
	configCacheMu    sync.Mutex
	configCache      *Config
	configCacheErr   error
	configCachePath  string
	configCacheMtime int64
)

type Config struct {
	Version        string      `toml:"version"`
	SchemaPath     string      `toml:"schema_path"`
	SchemaDir      string      `toml:"schema_dir"`
	Queries        string      `toml:"queries"`
	MigrationsPath string      `toml:"migrations_path"`
	ExportPath     string      `toml:"export_path"`
	Default        bool        `toml:"default"`
	EnvPath        string      `toml:"env_path"`
	Database       Database    `toml:"database"`
	Gen            Gen         `toml:"gen"`
	Cache          CacheConfig `toml:"cache"`
	JsonPath       string      `toml:"json_path"` // directory for JSON type definition files
	ForceRegen     bool        `toml:"-"`
	// Multi-database support
	Databases []DatabaseConfig `toml:"databases"`
	// ActiveDB is the database selected by --db flag (runtime only)
	ActiveDB string `toml:"-"`
}

// DatabaseConfig represents a single database in multi-db mode
type DatabaseConfig struct {
	Name           string `toml:"name"`
	Provider       string `toml:"provider"`
	URLEnv         string `toml:"url_env"`
	SchemaDir      string `toml:"schema_dir"`
	Queries        string `toml:"queries"`
	MigrationsPath string `toml:"migrations_path"`
	ExportPath     string `toml:"export_path"`
	Default        bool   `toml:"default"`
	Gen            Gen    `toml:"gen"`
}

type Database struct {
	Provider string `toml:"provider"`
	URLEnv   string `toml:"url_env"`
}

type Gen struct {
	Go     GoGen     `toml:"go"`
	JS     JSGen     `toml:"js"`
	Python PythonGen `toml:"python"`
	Kotlin KotlinGen `toml:"kotlin"`
	Java   JavaGen   `toml:"java"`
	Rust   RustGen   `toml:"rust"`
}

type GoGen struct {
	Enabled bool   `toml:"enabled"`
	Driver  string `toml:"driver"` // "database/sql" (default) or "pgx"
	Out     string `toml:"out"`
}

type JSGen struct {
	Enabled bool   `toml:"enabled"`
	Out     string `toml:"out"`
	Driver  string `toml:"driver"` // "pg" (default) or "postgres"
}

type PythonGen struct {
	Enabled bool   `toml:"enabled"`
	Out     string `toml:"out"`
	Async   bool   `toml:"async"`  // true = async (default), false = sync
	Driver  string `toml:"driver"` // database-specific driver
}

type KotlinGen struct {
	Enabled bool   `toml:"enabled"`
	Out     string `toml:"out"`
	Package string `toml:"package"` // e.g. "com.example.db" — used in package declaration and imports
	Driver  string `toml:"driver"`  // "jdbc" (default), "exposed", "r2dbc"
}

type JavaGen struct {
	Enabled bool   `toml:"enabled"`
	Out     string `toml:"out"`
	Package string `toml:"package"` // e.g. "com.example.db" — used in package declaration and imports
	Driver  string `toml:"driver"`  // "jdbc" (default), "jooq", "hibernate"
}

type RustGen struct {
	Enabled bool   `toml:"enabled"`
	Out     string `toml:"out"`
	Driver  string `toml:"driver"` // "sqlx" (default)
	Macros  bool   `toml:"macros"` // use sqlx compile-time checked macros (default: false)
}

// CacheConfig controls Redis-based query caching via @cache annotations
type CacheConfig struct {
	Enabled     bool   `toml:"enabled"`       // false by default — no cache code generated
	RedisURLEnv string `toml:"redis_url_env"` // env var name, default "REDIS_URL"
	DefaultTTL  string `toml:"default_ttl"`   // default TTL, e.g. "5m"
	Prefix      string `toml:"prefix"`        // key prefix, default "flash"
}

// rawPythonGen uses a pointer so we can detect whether "async" was explicitly set.
type rawPythonGen struct {
	Enabled bool   `toml:"enabled"`
	Out     string `toml:"out"`
	Async   *bool  `toml:"async"` // nil = not present in TOML
	Driver  string `toml:"driver"`
}

type rawGen struct {
	Go     GoGen        `toml:"go"`
	JS     JSGen        `toml:"js"`
	Python rawPythonGen `toml:"python"`
	Kotlin KotlinGen    `toml:"kotlin"`
	Java   JavaGen      `toml:"java"`
	Rust   RustGen      `toml:"rust"`
}

type rawConfig struct {
	Version        string           `toml:"version"`
	SchemaPath     string           `toml:"schema_path"`
	SchemaDir      string           `toml:"schema_dir"`
	Queries        string           `toml:"queries"`
	MigrationsPath string           `toml:"migrations_path"`
	ExportPath     string           `toml:"export_path"`
	Default        bool             `toml:"default"`
	EnvPath        string           `toml:"env_path"`
	JsonPath       string           `toml:"json_path"`
	Database       Database         `toml:"database"`
	Gen            rawGen           `toml:"gen"`
	Cache          CacheConfig      `toml:"cache"`
	Databases      []DatabaseConfig `toml:"databases"`
}

// Load reads and returns the config, with in-memory caching keyed by file path
// and modification time. This avoids redundant disk I/O when Load() is called
// multiple times within a single command invocation.
func Load() (*Config, error) {
	path := ConfigFile
	if path == "" {
		path = "flash.toml"
	}

	// Resolve to absolute path for reliable cache keying
	if absPath, err := filepath.Abs(path); err == nil {
		path = absPath
	}

	// Check file mod time for cache invalidation
	var mtime int64
	if fi, err := os.Stat(path); err == nil {
		mtime = fi.ModTime().UnixNano()
	}

	configCacheMu.Lock()
	defer configCacheMu.Unlock()

	if configCache != nil && configCachePath == path && configCacheMtime == mtime && configCacheErr == nil {
		// Return a shallow copy so callers can't mutate the cache
		cfg := *configCache
		return &cfg, nil
	}

	cfg, err := loadUncached()
	configCache = cfg
	configCacheErr = err
	configCachePath = path
	configCacheMtime = mtime
	if err != nil {
		return nil, err
	}
	// Return a copy so callers can't mutate the cache
	copyCfg := *cfg
	return &copyCfg, nil
}

// ResetConfigCache clears the in-memory config cache. It is intended for tests
// that need to reload config from a different file or working directory.
func ResetConfigCache() {
	configCacheMu.Lock()
	defer configCacheMu.Unlock()
	configCache = nil
	configCacheErr = nil
	configCachePath = ""
	configCacheMtime = 0
}

// loadUncached performs the actual config loading without caching.
func loadUncached() (*Config, error) {
	var cfg Config

	path := ConfigFile
	if path == "" {
		path = "flash.toml"
	}

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	pythonAsyncSet := false
	if data != nil {
		var raw rawConfig
		if err := toml.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("failed to parse config: %w", err)
		}
		// Copy raw values into the main config struct
		cfg.Version = raw.Version
		cfg.SchemaPath = raw.SchemaPath
		cfg.SchemaDir = raw.SchemaDir
		cfg.Queries = raw.Queries
		cfg.MigrationsPath = raw.MigrationsPath
		cfg.ExportPath = raw.ExportPath
		cfg.EnvPath = raw.EnvPath
		cfg.JsonPath = raw.JsonPath
		cfg.Database = raw.Database
		cfg.Databases = raw.Databases
		cfg.Gen.Go = raw.Gen.Go
		cfg.Gen.JS = raw.Gen.JS
		cfg.Gen.Python.Enabled = raw.Gen.Python.Enabled
		cfg.Gen.Python.Out = raw.Gen.Python.Out
		cfg.Gen.Python.Driver = raw.Gen.Python.Driver
		cfg.Gen.Kotlin = raw.Gen.Kotlin
		cfg.Gen.Java = raw.Gen.Java
		cfg.Gen.Rust = raw.Gen.Rust
		cfg.Cache = raw.Cache
		if raw.Gen.Python.Async != nil {
			cfg.Gen.Python.Async = *raw.Gen.Python.Async
			pythonAsyncSet = true
		}
	}

	// Set defaults
	if cfg.Version == "" {
		cfg.Version = "2"
	}
	// Support both old schema_path and new schema_dir
	if cfg.SchemaDir == "" {
		if cfg.SchemaPath != "" {
			// Legacy: if schema_path is a file, use its directory
			// If it looks like a directory (no .sql extension), use it directly
			if strings.HasSuffix(cfg.SchemaPath, ".sql") {
				cfg.SchemaDir = filepath.Dir(cfg.SchemaPath)
			} else {
				cfg.SchemaDir = cfg.SchemaPath
			}
		} else {
			cfg.SchemaDir = "db/schema"
		}
	}
	// Keep SchemaPath for backward compatibility
	if cfg.SchemaPath == "" {
		cfg.SchemaPath = filepath.Join(cfg.SchemaDir, "schema.sql")
	}
	if cfg.Queries == "" {
		cfg.Queries = "db/queries/"
	}
	if cfg.MigrationsPath == "" {
		cfg.MigrationsPath = "db/migrations"
	}
	if cfg.ExportPath == "" {
		cfg.ExportPath = "db/export"
	}
	if cfg.Database.Provider == "" {
		cfg.Database.Provider = "postgresql"
	}
	if cfg.Database.URLEnv == "" {
		cfg.Database.URLEnv = "DATABASE_URL"
	}
	if cfg.Gen.JS.Out == "" && cfg.Gen.JS.Enabled {
		cfg.Gen.JS.Out = "flash_gen"
	}
	if cfg.Gen.Python.Out == "" && cfg.Gen.Python.Enabled {
		cfg.Gen.Python.Out = "flash_gen"
	}
	if cfg.Gen.Go.Out == "" {
		cfg.Gen.Go.Out = "flash_gen"
	}
	if cfg.Gen.Kotlin.Out == "" && cfg.Gen.Kotlin.Enabled {
		if cfg.Gen.Kotlin.Package != "" {
			cfg.Gen.Kotlin.Out = "src/main/kotlin/" + strings.ReplaceAll(cfg.Gen.Kotlin.Package, ".", "/")
		} else {
			cfg.Gen.Kotlin.Out = "flash_gen"
		}
	}
	if cfg.Gen.Java.Out == "" && cfg.Gen.Java.Enabled {
		if cfg.Gen.Java.Package != "" {
			cfg.Gen.Java.Out = "src/main/java/" + strings.ReplaceAll(cfg.Gen.Java.Package, ".", "/")
		} else {
			cfg.Gen.Java.Out = "flash_gen"
		}
	}
	if cfg.Gen.Python.Enabled && !pythonAsyncSet {
		cfg.Gen.Python.Async = true
	}
	if cfg.Gen.Rust.Out == "" && cfg.Gen.Rust.Enabled {
		cfg.Gen.Rust.Out = "src/flash_gen"
	}
	if cfg.Gen.Rust.Driver == "" && cfg.Gen.Rust.Enabled {
		cfg.Gen.Rust.Driver = "sqlx"
	}

	// Cache defaults
	if cfg.Cache.Enabled {
		if cfg.Cache.RedisURLEnv == "" {
			cfg.Cache.RedisURLEnv = "REDIS_URL"
		}
		if cfg.Cache.DefaultTTL == "" {
			cfg.Cache.DefaultTTL = "5m"
		}
		if cfg.Cache.Prefix == "" {
			cfg.Cache.Prefix = "flash"
		}
	}

	return &cfg, nil
}

func (c *Config) GetDatabaseURL() (string, error) {
	dbURL := os.Getenv(c.Database.URLEnv)
	if dbURL == "" {
		return "", fmt.Errorf("database URL not found in environment variable %s", c.Database.URLEnv)
	}
	return dbURL, nil
}

// IsMultiDB returns true if the config uses the multi-database format.
func (c *Config) IsMultiDB() bool {
	return len(c.Databases) > 0
}

// ListDatabases returns all database names configured.
func (c *Config) ListDatabases() []string {
	if !c.IsMultiDB() {
		return []string{"default"}
	}
	names := make([]string, len(c.Databases))
	for i, db := range c.Databases {
		names[i] = db.Name
	}
	return names
}

// GetDefaultDB returns the name of the database marked as default.
// If none is marked, returns empty string (meaning: run all).
func (c *Config) GetDefaultDB() string {
	for _, db := range c.Databases {
		if db.Default {
			return db.Name
		}
	}
	return ""
}

// ResolveForDB returns a Config resolved for a specific database name.
// For single-db configs (no [[databases]]), returns self unchanged.
// For multi-db configs, overrides paths/database/gen from the named database.
func (c *Config) ResolveForDB(name string) (*Config, error) {
	if !c.IsMultiDB() {
		return c, nil
	}
	for _, db := range c.Databases {
		if db.Name == name {
			resolved := *c
			resolved.Database = Database{Provider: db.Provider, URLEnv: db.URLEnv}
			if db.SchemaDir != "" {
				resolved.SchemaDir = db.SchemaDir
			}
			if db.Queries != "" {
				resolved.Queries = db.Queries
			}
			if db.MigrationsPath != "" {
				resolved.MigrationsPath = db.MigrationsPath
			}
			if db.ExportPath != "" {
				resolved.ExportPath = db.ExportPath
			}
			resolved.Gen = db.Gen
			resolved.ActiveDB = name
			return &resolved, nil
		}
	}
	return nil, fmt.Errorf("database %q not found in config. Available: %v", name, c.ListDatabases())
}

func (c *Config) EnsureDirectories() error {
	dirs := []string{
		c.MigrationsPath,
		c.GetSchemaDir(),
	}

	for _, dir := range dirs {
		if dir == "" || dir == "." {
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	return nil
}

func (c *Config) Validate() error {
	supportedProviders := []string{"postgresql", "postgres", "mysql", "sqlite", "sqlite3", "clickhouse", "scylla", "scylladb", "cassandra"}
	supported := slices.Contains(supportedProviders, c.Database.Provider)
	if !supported {
		return fmt.Errorf("unsupported database provider: %s. Supported providers: %v", c.Database.Provider, supportedProviders)
	}

	if c.MigrationsPath == "" {
		return fmt.Errorf("migrations_path cannot be empty")
	}

	if c.ExportPath == "" {
		return fmt.Errorf("export_path cannot be empty")
	}

	return nil
}

func (c *Config) GetSqlcEngine() string {
	switch c.Database.Provider {
	case "postgresql", "postgres":
		return "postgresql"
	case "mysql":
		return "mysql"
	case "sqlite", "sqlite3":
		return "sqlite"
	case "clickhouse":
		return "clickhouse"
	case "scylla", "scylladb", "cassandra":
		return "scylla"
	default:
		return "postgresql"
	}
}

func (c *Config) GetSchemaDir() string {
	if c.SchemaDir != "" {
		return c.SchemaDir
	}
	return filepath.Dir(c.SchemaPath)
}

// GetSchemaFiles returns all .sql files in the schema directory
func (c *Config) GetSchemaFiles() ([]string, error) {
	schemaDir := c.GetSchemaDir()

	entries, err := os.ReadDir(schemaDir)
	if err != nil {
		// If directory doesn't exist, check if schema_path is a file
		if os.IsNotExist(err) && c.SchemaPath != "" {
			if _, err := os.Stat(c.SchemaPath); err == nil {
				return []string{c.SchemaPath}, nil
			}
		}
		return nil, fmt.Errorf("failed to read schema directory %s: %w", schemaDir, err)
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && (strings.HasSuffix(entry.Name(), ".sql") || strings.HasSuffix(entry.Name(), ".cql")) {
			files = append(files, filepath.Join(schemaDir, entry.Name()))
		}
	}

	// Sort files for consistent ordering
	// Files are typically named like: 001_users.sql, 002_posts.sql or users.sql, posts.sql
	slices.Sort(files)
	return files, nil
}

func (c *Config) IsNodeProject() bool {
	_, err := os.Stat("package.json")
	return err == nil
}
