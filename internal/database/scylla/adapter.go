package scylla

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	"github.com/Lumos-Labs-HQ/flash/internal/database/common"
	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

type schemaCache struct {
	mu      sync.RWMutex
	tables  map[string][]types.SchemaColumn
	expires time.Time
}

func (c *schemaCache) get(key string) ([]types.SchemaColumn, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if time.Now().After(c.expires) {
		return nil, false
	}
	cols, ok := c.tables[key]
	return cols, ok
}

func (c *schemaCache) set(key string, cols []types.SchemaColumn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tables == nil {
		c.tables = make(map[string][]types.SchemaColumn)
	}
	c.tables[key] = cols
	c.expires = time.Now().Add(10 * time.Second)
}

func (c *schemaCache) invalidate() {
	c.mu.Lock()
	c.tables = nil
	c.mu.Unlock()
}

type Adapter struct {
	session  *gocql.Session
	cluster  *gocql.ClusterConfig
	keyspace string
	cache    schemaCache
}

func New() *Adapter { return &Adapter{} }

var typeMap = map[string]string{
	"ascii": "ASCII", "text": "TEXT", "varchar": "VARCHAR",
	"int": "INT", "bigint": "BIGINT", "smallint": "SMALLINT",
	"tinyint": "TINYINT", "varint": "VARINT", "float": "FLOAT",
	"double": "DOUBLE", "boolean": "BOOLEAN", "bool": "BOOLEAN",
	"decimal": "DECIMAL", "counter": "COUNTER", "timestamp": "TIMESTAMP",
	"uuid": "UUID", "timeuuid": "TIMEUUID", "inet": "INET",
	"blob": "BLOB", "date": "DATE", "time": "TIME",
	"duration": "DURATION", "map": "MAP", "list": "LIST",
	"set": "SET", "tuple": "TUPLE", "frozen": "FROZEN",
}

// Connect establishes a connection to ScyllaDB/Cassandra.
//
// URL: scylla://[user:pass@]host:9042[,host2:9042]/keyspace[?params]
//
// Params: consistency, dc, token_aware, num_conns, page_size, timeout,
//
//	connect_timeout, keepalive, protocol_version, ssl, disable_host_lookup
func (a *Adapter) Connect(ctx context.Context, urlStr string) error {
	cleanURL := strings.TrimPrefix(urlStr, "scylla://")

	var username, password, hosts, keyspace string
	queryParams := url.Values{}

	if parsed, err := url.Parse("scylla://" + cleanURL); err == nil && parsed.Host != "" {
		hosts = parsed.Host
		if parsed.User != nil {
			username = parsed.User.Username()
			password, _ = parsed.User.Password()
		}
		keyspace = strings.TrimPrefix(parsed.Path, "/")
		queryParams = parsed.Query()
	} else {
		if before, after, ok := strings.Cut(cleanURL, "/"); ok {
			path := after
			hosts = before
			if before, after, ok := strings.Cut(path, "?"); ok {
				keyspace = before
				queryParams, _ = url.ParseQuery(after)
			} else {
				keyspace = path
			}
		} else {
			hosts = cleanURL
		}
	}

	hostList := parseHosts(hosts)
	if len(hostList) == 0 {
		return fmt.Errorf("no valid ScyllaDB hosts specified")
	}

	cluster := gocql.NewCluster(hostList...)
	cluster.Consistency = gocql.One
	cluster.Timeout = 10 * time.Second
	cluster.ConnectTimeout = 5 * time.Second
	cluster.NumConns = 1
	cluster.ProtoVersion = 4 // Skip protocol negotiation round-trips (~7s saved on remote hosts)
	// Skip peer discovery and topology event subscriptions — this is a CLI tool
	// that runs one command then exits. These save ~10s of connection overhead.
	cluster.DisableInitialHostLookup = true
	cluster.IgnorePeerAddr = true
	cluster.Events.DisableNodeStatusEvents = true
	cluster.Events.DisableTopologyEvents = true
	cluster.Events.DisableSchemaEvents = true
	cluster.ReconnectInterval = 0

	applyIntParam(queryParams, "protocol_version", func(v int) { cluster.ProtoVersion = v })
	applyDurationParam(queryParams, "timeout", func(d time.Duration) { cluster.Timeout = d })
	applyDurationParam(queryParams, "connect_timeout", func(d time.Duration) { cluster.ConnectTimeout = d })
	applyDurationParam(queryParams, "keepalive", func(d time.Duration) { cluster.SocketKeepalive = d })
	applyIntParam(queryParams, "num_conns", func(v int) { cluster.NumConns = v })
	applyIntParam(queryParams, "page_size", func(v int) { cluster.PageSize = v })
	applyBoolParam(queryParams, "disable_host_lookup", func(v bool) {
		cluster.DisableInitialHostLookup = v
		cluster.IgnorePeerAddr = v
	})
	if c := queryParams.Get("consistency"); c != "" {
		cluster.Consistency = parseConsistency(c)
	}

	dc := queryParams.Get("dc")
	tokenAware := parseBoolDefault(queryParams.Get("token_aware"), true)
	cluster.PoolConfig.HostSelectionPolicy = buildHostSelectionPolicy(dc, tokenAware)

	if username != "" {
		cluster.Authenticator = gocql.PasswordAuthenticator{Username: username, Password: password}
	}
	if parseBoolDefault(queryParams.Get("ssl"), false) {
		cluster.SslOpts = &gocql.SslOptions{EnableHostVerification: false}
	}

	// If no keyspace given, auto-create "flash".
	// Connect without keyspace, create it, reconnect with keyspace set.
	needsAutoKeyspace := keyspace == ""
	if needsAutoKeyspace {
		keyspace = "flash"
	}

	if needsAutoKeyspace {
		// Connect once without keyspace, create it if needed, then reconnect.
		cluster.Keyspace = keyspace
		if s, err := cluster.CreateSession(); err == nil {
			// Keyspace exists — skip the auto-create path entirely.
			a.session = s
			a.cluster = cluster
			a.keyspace = keyspace
			return nil
		}
		// Keyspace doesn't exist — create it via a no-keyspace session.
		cluster.Keyspace = ""
		s0, err := cluster.CreateSession()
		if err != nil {
			return fmt.Errorf("failed to connect to ScyllaDB: %w", err)
		}
		err = s0.Query(fmt.Sprintf(
			`CREATE KEYSPACE IF NOT EXISTS "%s" WITH REPLICATION = {'class': 'SimpleStrategy', 'replication_factor': 1}`,
			keyspace,
		)).ExecContext(ctx)
		s0.Close()
		if err != nil {
			return fmt.Errorf("failed to create keyspace '%s': %w", keyspace, err)
		}
		cluster.PoolConfig.HostSelectionPolicy = buildHostSelectionPolicy(dc, tokenAware)
	}

	cluster.Keyspace = keyspace
	session, err := cluster.CreateSession()
	if err != nil {
		return fmt.Errorf("failed to connect to ScyllaDB: %w", err)
	}

	a.session = session
	a.cluster = cluster
	a.keyspace = keyspace
	return nil
}

func parseHosts(raw string) []string {
	var result []string
	for p := range strings.SplitSeq(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, ":") {
			p += ":9042"
		}
		result = append(result, p)
	}
	return result
}

func applyIntParam(params url.Values, key string, set func(int)) {
	if v := params.Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			set(n)
		}
	}
}

func applyDurationParam(params url.Values, key string, set func(time.Duration)) {
	if v := params.Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			set(time.Duration(n) * time.Second)
		}
	}
}

func applyBoolParam(params url.Values, key string, set func(bool)) {
	if v := params.Get(key); v != "" {
		set(strings.EqualFold(v, "true") || v == "1")
	}
}

func parseBoolDefault(v string, def bool) bool {
	if v == "" {
		return def
	}
	return strings.EqualFold(v, "true") || v == "1"
}

func parseConsistency(c string) gocql.Consistency {
	switch strings.ToLower(c) {
	case "one":
		return gocql.One
	case "two":
		return gocql.Two
	case "three":
		return gocql.Three
	case "all":
		return gocql.All
	case "localquorum", "local_quorum":
		return gocql.LocalQuorum
	case "eachquorum", "each_quorum":
		return gocql.EachQuorum
	case "localone", "local_one":
		return gocql.LocalOne
	case "any":
		return gocql.Any
	default:
		return gocql.Quorum
	}
}

func buildHostSelectionPolicy(dc string, tokenAware bool) gocql.HostSelectionPolicy {
	var base gocql.HostSelectionPolicy
	if dc != "" {
		base = gocql.DCAwareRoundRobinPolicy(dc)
	} else {
		base = gocql.RoundRobinHostPolicy()
	}
	if tokenAware {
		return gocql.TokenAwareHostPolicy(base)
	}
	return base
}

func (a *Adapter) Close() error {
	if a.session != nil {
		a.session.Close()
	}
	return nil
}

func (a *Adapter) Ping(ctx context.Context) error {
	return a.session.Query("SELECT release_version FROM system.local").ExecContext(ctx)
}

func (a *Adapter) CreateMigrationsTable(ctx context.Context) error {
	return a.session.Query(
		`CREATE TABLE IF NOT EXISTS "_flash_migrations" (id text, migration_name text, checksum text, started_at timestamp, finished_at timestamp, applied_steps_count int, PRIMARY KEY (id))`,
	).ExecContext(ctx)
}

func (a *Adapter) EnsureMigrationTableCompatibility(_ context.Context) error { return nil }

func (a *Adapter) CleanupBrokenMigrationRecords(ctx context.Context) error {
	iter := a.session.Query(`SELECT id, started_at, finished_at FROM "_flash_migrations"`).IterContext(ctx)
	defer iter.Close()

	staleThreshold := time.Now().Add(-1 * time.Hour)
	var stale []string
	var id string
	var startedAt, finishedAt time.Time
	for iter.Scan(&id, &startedAt, &finishedAt) {
		if finishedAt.IsZero() && startedAt.Before(staleThreshold) {
			stale = append(stale, id)
		}
	}
	if err := iter.Close(); err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return nil
		}
		return err
	}
	for _, sid := range stale {
		if err := a.session.Query(`DELETE FROM "_flash_migrations" WHERE id = ?`, sid).ExecContext(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) GetAppliedMigrations(ctx context.Context) (map[string]*time.Time, error) {
	iter := a.session.Query(`SELECT id, finished_at FROM "_flash_migrations"`).IterContext(ctx)
	defer iter.Close()

	applied := make(map[string]*time.Time)
	var id string
	var finishedAt time.Time
	for iter.Scan(&id, &finishedAt) {
		if finishedAt.IsZero() {
			continue
		}
		t := finishedAt
		applied[id] = &t
	}
	if err := iter.Close(); err != nil {
		if strings.Contains(err.Error(), "does not exist") || strings.Contains(err.Error(), "not found") {
			return make(map[string]*time.Time), nil
		}
		return nil, err
	}
	return applied, nil
}

func (a *Adapter) RecordMigration(ctx context.Context, migrationID, name, checksum string) error {
	now := time.Now()
	return a.session.Query(
		`INSERT INTO "_flash_migrations" (id, migration_name, checksum, started_at, finished_at, applied_steps_count) VALUES (?, ?, ?, ?, ?, 1)`,
		migrationID, name, checksum, now, now,
	).ExecContext(ctx)
}

func (a *Adapter) RemoveMigrationRecord(ctx context.Context, migrationID string) error {
	return a.session.Query(`DELETE FROM "_flash_migrations" WHERE id = ?`, migrationID).ExecContext(ctx)
}

func (a *Adapter) ExecuteMigration(ctx context.Context, migrationSQL string) error {
	stmts := common.ParseSQLStatements(migrationSQL)
	var filtered []string
	for _, s := range stmts {
		if s = strings.TrimSpace(s); s != "" {
			filtered = append(filtered, s)
		}
	}
	if len(filtered) == 0 {
		return nil
	}

	type group struct {
		parallel bool
		stmts    []string
	}
	groups := []group{{parallel: isIndependentDDL(filtered[0]), stmts: []string{filtered[0]}}}
	for _, stmt := range filtered[1:] {
		ind := isIndependentDDL(stmt)
		last := &groups[len(groups)-1]
		if last.parallel == ind {
			last.stmts = append(last.stmts, stmt)
		} else {
			groups = append(groups, group{parallel: ind, stmts: []string{stmt}})
		}
	}

	for _, g := range groups {
		if !g.parallel || len(g.stmts) == 1 {
			for _, stmt := range g.stmts {
				if err := a.session.Query(stmt).ExecContext(ctx); err != nil {
					return fmt.Errorf("scylla: %w", err)
				}
			}
			continue
		}
		errCh := make(chan error, len(g.stmts))
		for _, stmt := range g.stmts {
			go func(s string) { errCh <- a.session.Query(s).ExecContext(ctx) }(stmt)
		}
		for range g.stmts {
			if err := <-errCh; err != nil {
				return fmt.Errorf("scylla: %w", err)
			}
		}
	}
	return nil
}

func isIndependentDDL(stmt string) bool {
	upper := strings.ToUpper(strings.TrimSpace(stmt))
	// Only CREATE TABLE / DROP TABLE can safely run in parallel.
	// CREATE INDEX and CREATE MATERIALIZED VIEW depend on tables already existing.
	// CREATE TYPE / DROP TYPE must complete before tables that reference them.
	return strings.HasPrefix(upper, "CREATE TABLE") || strings.HasPrefix(upper, "DROP TABLE")
}

func (a *Adapter) ExecuteAndRecordMigration(ctx context.Context, migrationID, name, checksum, migrationSQL string) error {
	if migrationSQL != "" {
		if err := a.ExecuteMigration(ctx, migrationSQL); err != nil {
			return err
		}
	}
	a.cache.invalidate()
	return a.RecordMigration(ctx, migrationID, name, checksum)
}

func (a *Adapter) ExecuteQuery(ctx context.Context, query string) (*common.QueryResult, error) {
	return a.ExecuteQueryWithArgs(ctx, query)
}

func (a *Adapter) ExecuteQueryWithArgs(ctx context.Context, query string, args ...any) (*common.QueryResult, error) {
	iter := a.session.Query(query, args...).IterContext(ctx)
	defer iter.Close()

	cols := iter.Columns()
	if len(cols) == 0 {
		return &common.QueryResult{}, iter.Close()
	}

	colNames := make([]string, len(cols))
	for i, c := range cols {
		colNames[i] = c.Name
	}

	var results []map[string]any
	for {
		m := make(map[string]any)
		if !iter.MapScan(m) {
			break
		}
		results = append(results, m)
	}
	return &common.QueryResult{Columns: colNames, Rows: results}, iter.Close()
}

func (a *Adapter) ExecuteDMLWithArgs(ctx context.Context, query string, args ...any) error {
	return a.session.Query(query, args...).ExecContext(ctx)
}

func (a *Adapter) QuoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func (a *Adapter) ProviderName() string { return "scylla" }

func (a *Adapter) MapColumnType(dbType string) string {
	t := strings.TrimSpace(dbType)
	if idx := strings.Index(t, "<"); idx >= 0 {
		base := strings.ToLower(t[:idx])
		if mapped, ok := typeMap[base]; ok {
			return mapped + t[idx:]
		}
		return strings.ToUpper(base) + t[idx:]
	}
	if mapped, ok := typeMap[strings.ToLower(t)]; ok {
		return mapped
	}
	return strings.ToUpper(t)
}

func (a *Adapter) currentKeyspace() string {
	if a.keyspace != "" {
		return a.keyspace
	}
	return "system"
}

// CurrentKeyspace returns the active keyspace (exported for hints).
func (a *Adapter) CurrentKeyspace() string { return a.currentKeyspace() }

func sanitizeKeyspace(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}
