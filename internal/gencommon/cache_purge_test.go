package gencommon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// setupPruneFixture creates a queries dir with one live file and a cache
// holding entries for the live file plus one deleted file.
func setupPruneFixture(t *testing.T) (*GenerationCache, string) {
	t.Helper()
	dir := t.TempDir()
	queries := filepath.Join(dir, "queries")
	if err := os.MkdirAll(queries, 0755); err != nil {
		t.Fatal(err)
	}
	// Live file
	if err := os.WriteFile(filepath.Join(queries, "users.sql"), []byte("-- name: A :one\nSELECT 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Deleted file — only its cache entries remain
	deletedPath := filepath.Join(queries, "old.sql")

	c := NewGenerationCacheWithDir(filepath.Join(dir, "flash_gen"))
	c.UpdateQueryChecksum(filepath.Join(queries, "users.sql"), "hash-live")
	c.UpdateQueryChecksum(deletedPath, "hash-dead")
	c.UpdateQueryDependencies(deletedPath, []string{"old_table"})
	// Generated-file entries are keyed by OUTPUT path (see UpdateCacheForFile).
	c.UpdateGeneratedFileChecksum(filepath.Join(dir, "flash_gen", "old.go"), "dead-output")
	c.UpdateGeneratedFileChecksum(filepath.Join(dir, "flash_gen", "users.go"), "live-output")
	return c, queries
}

func TestPruneStaleQueryEntries_RemovesDeletedFileEntries(t *testing.T) {
	c, queries := setupPruneFixture(t)
	pruned := c.PruneStaleQueryEntries(queries)
	if len(pruned) != 1 || pruned[0] != filepath.Join(queries, "old.sql") {
		t.Fatalf("pruned = %v, want [old.sql path]", pruned)
	}
	if _, ok := c.QueryFileChecksums[filepath.Join(queries, "old.sql")]; ok {
		t.Error("stale checksum entry survived prune")
	}
	if _, ok := c.QueryTableDeps[filepath.Join(queries, "old.sql")]; ok {
		t.Error("stale deps entry survived prune")
	}
	if _, ok := c.GeneratedFileChecksums[filepath.Join(c.OutDir, "old.go")]; ok {
		t.Error("stale generated-file entry survived prune")
	}
	if _, ok := c.GeneratedFileChecksums[filepath.Join(c.OutDir, "users.go")]; !ok {
		t.Error("live generated-file entry must survive prune")
	}
}

func TestPruneStaleQueryEntries_KeepsLiveEntries(t *testing.T) {
	c, queries := setupPruneFixture(t)
	c.PruneStaleQueryEntries(queries)
	livePath := filepath.Join(queries, "users.sql")
	if _, ok := c.QueryFileChecksums[livePath]; !ok {
		t.Error("live checksum entry was pruned")
	}
}

func TestPruneStaleQueryEntries_RenamedFileIsStale(t *testing.T) {
	// Rename = delete + add: entries under the old path must go.
	c, queries := setupPruneFixture(t)
	c.UpdateQueryChecksum(filepath.Join(queries, "renamed_away.sql"), "hash")
	// Simulate rename on disk: users.sql → members.sql
	if err := os.Rename(filepath.Join(queries, "users.sql"), filepath.Join(queries, "members.sql")); err != nil {
		t.Fatal(err)
	}
	pruned := c.PruneStaleQueryEntries(queries)
	joined := map[string]bool{}
	for _, p := range pruned {
		joined[p] = true
	}
	if !joined[filepath.Join(queries, "old.sql")] {
		t.Errorf("pruned = %v, must include old.sql", pruned)
	}
	if !joined[filepath.Join(queries, "renamed_away.sql")] {
		t.Errorf("pruned = %v, must include renamed_away.sql", pruned)
	}
	if !joined[filepath.Join(queries, "users.sql")] {
		t.Errorf("pruned = %v, must include users.sql (file renamed away)", pruned)
	}
}

func TestPruneStaleQueryEntries_CQLEntriesHandled(t *testing.T) {
	dir := t.TempDir()
	queries := filepath.Join(dir, "queries")
	if err := os.MkdirAll(queries, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queries, "events.cql"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	c := NewGenerationCacheWithDir(filepath.Join(dir, "flash_gen"))
	c.UpdateQueryChecksum(filepath.Join(queries, "events.cql"), "h")
	// A stale .cql entry (file since deleted) vs the live one.
	c.UpdateQueryChecksum(filepath.Join(queries, "gone.cql"), "h")
	pruned := c.PruneStaleQueryEntries(queries)
	if len(pruned) != 1 || pruned[0] != filepath.Join(queries, "gone.cql") {
		t.Fatalf("pruned = %v, want [gone.cql]", pruned)
	}
}

func TestPruneStaleQueryEntries_BareNameKeys(t *testing.T) {
	// Some cache users store bare source names ("users") rather than paths —
	// the prune must resolve them against the queries dir with known exts.
	dir := t.TempDir()
	queries := filepath.Join(dir, "queries")
	if err := os.MkdirAll(queries, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queries, "users.sql"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	c := NewGenerationCacheWithDir(filepath.Join(dir, "flash_gen"))
	c.UpdateQueryChecksum("users", "h")
	c.UpdateQueryChecksum("ghost", "h")
	pruned := c.PruneStaleQueryEntries(queries)
	if len(pruned) != 1 || pruned[0] != "ghost" {
		t.Fatalf("pruned = %v, want [ghost]", pruned)
	}
}

func TestPruneStaleQueryEntries_EmptyQueriesDirKeepsUnverifiableEntries(t *testing.T) {
	// When the queries dir is unknown (""), only absolute path keys can be
	// verified against the filesystem. Bare names and relative paths must be
	// kept — pruning on a guess would wipe valid caches.
	c, queries := setupPruneFixture(t)
	// Add a bare-name entry and a relative-path entry that cannot be verified.
	c.UpdateQueryChecksum("bare", "h")
	c.UpdateQueryChecksum("rel/old.sql", "h")
	pruned := c.PruneStaleQueryEntries("")
	// The absolute stale path IS verifiable (file gone) → pruned.
	found := false
	for _, p := range pruned {
		if p == filepath.Join(queries, "old.sql") {
			found = true
		}
	}
	if !found {
		t.Errorf("pruned = %v, must include the absolute stale path", pruned)
	}
	// Unverifiable keys must be kept.
	if _, ok := c.QueryFileChecksums["bare"]; !ok {
		t.Error("bare-name entry must be kept when queries dir is unknown")
	}
	if _, ok := c.QueryFileChecksums["rel/old.sql"]; !ok {
		t.Error("relative-path entry must be kept when queries dir is unknown")
	}
}

func TestPruneStaleQueryEntries_PersistedAfterSaveLoad(t *testing.T) {
	// Pruned state must survive a Save/Load round-trip — otherwise the next
	// run resurrects stale entries from disk.
	c, queries := setupPruneFixture(t)
	c.MarkGeneration()
	c.PruneStaleQueryEntries(queries)
	if err := c.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded := NewGenerationCacheWithDir(c.OutDir)
	if _, ok := loaded.QueryFileChecksums[filepath.Join(queries, "old.sql")]; ok {
		t.Error("stale entry resurrected by Load after prune+save")
	}
}

// ── GenerationCache behavior previously untested ─────────────────────────────

func TestGenerationCache_LoadVersionMismatchResets(t *testing.T) {
	dir := t.TempDir()
	c := NewGenerationCacheWithDir(dir)
	c.UpdateQueryChecksum("users.sql", "h")
	c.UpdateSchemaChecksum("sh")
	c.UpdateConfigChecksum("ch")
	c.LastGeneration = time.Now()
	if err := c.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Corrupt the version on disk
	path := filepath.Join(dir, cacheFileName)
	data, _ := os.ReadFile(path)
	bad := string(data)
	bad = replaceFirst(bad, `"version": "1.0"`, `"version": "9.9"`)
	if bad == string(data) {
		t.Fatal("could not rewrite version in cache file — format changed?")
	}
	if err := os.WriteFile(path, []byte(bad), 0644); err != nil {
		t.Fatal(err)
	}
	loaded := NewGenerationCacheWithDir(dir)
	if _, ok := loaded.QueryFileChecksums["users.sql"]; ok {
		t.Error("version-mismatch cache should have reset query checksums")
	}
	if loaded.SchemaChecksum != "" || loaded.ConfigChecksum != "" {
		t.Error("version-mismatch cache should have reset schema/config checksums")
	}
	if loaded.Version != "1.0" {
		t.Errorf("Version = %q, want 1.0 (reset)", loaded.Version)
	}
}

func TestGenerationCache_LoadCorruptJSONReturnsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, cacheFileName), []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	c := NewGenerationCacheWithDir(dir)
	if err := c.Load(); err == nil {
		t.Error("Load should return an error for corrupt JSON")
	}
}

func TestGenerationCache_SchemaFileDeletionChangesChecksum(t *testing.T) {
	dir := t.TempDir()
	c := NewGenerationCacheWithDir(filepath.Join(dir, "out"))
	h1, err := c.ComputeSchemaChecksum(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.sql"), []byte("CREATE TABLE a (id INT);"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.sql"), []byte("CREATE TABLE b (id INT);"), 0644); err != nil {
		t.Fatal(err)
	}
	h2, _ := c.ComputeSchemaChecksum(dir)
	if h1 == h2 {
		t.Error("checksum must change when schema files are added")
	}
	// Delete one file: content AND file-name set change.
	if err := os.Remove(filepath.Join(dir, "b.sql")); err != nil {
		t.Fatal(err)
	}
	h3, _ := c.ComputeSchemaChecksum(dir)
	if h3 == h2 {
		t.Error("checksum must change when a schema file is deleted")
	}
}

func TestGenerationCache_ConcurrentAccess(t *testing.T) {
	// The RWMutex-guarded cache is shared across generation workers.
	c := NewGenerationCacheWithDir(t.TempDir())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 500 {
			c.UpdateQueryChecksum("f.sql", "h")
			c.ShouldRegenerateQuery("f.sql", "h")
			c.UpdateQueryDependencies("f.sql", []string{"t"})
		}
	}()
	for range 500 {
		c.ShouldRegenerateAll("sh", "ch")
		c.PruneStaleQueryEntries("")
	}
	<-done
}

func replaceFirst(s, old, new string) string {
	idx := indexOf(s, old)
	if idx < 0 {
		return s
	}
	return s[:idx] + new + s[idx+len(old):]
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
