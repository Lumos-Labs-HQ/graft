package gencommon

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// FileResult is the output of a single file generation.
type FileResult struct {
	SourceFile string // e.g. "users"
	OutputFile string // e.g. "users.go" or "UsersQueries.java"
	Err        error
}

// FileGeneratorFunc generates code for a single query source file.
// Returns the generated code content and the output filename.
type FileGeneratorFunc func(sourceFile string, queries []any, fullRegen bool) (code string, outputFile string, err error)

// IncrementalPipeline runs parallel code generation with incremental caching.
type IncrementalPipeline struct {
	QueriesDir   string                 // directory containing .sql query files
	OutDir       string                 // output directory
	Extension    string                 // file extension for output (".go", ".java", ".kt", ".js", ".py")
	SourceMapper func(query any) string // extracts source file name from a query

	Cache *GenerationCache
}

// GroupQueries groups queries by their source file name.
func (p *IncrementalPipeline) GroupQueries(queries []any) map[string][]any {
	groups := make(map[string][]any)
	for _, q := range queries {
		src := p.SourceMapper(q)
		if src == "" {
			src = "queries"
		}
		groups[src] = append(groups[src], q)
	}
	return groups
}

// Run executes parallel code generation for all query groups.
// genFunc implements the actual code generation per file.
// It's called for each unique source file.
func (p *IncrementalPipeline) Run(queries []any, fullRegen bool, genFunc func(sourceFile string, fileQueries []any, fullRegen bool) (string, error)) error {
	groups := p.GroupQueries(queries)

	type fileGroup struct {
		sourceFile string
		queries    []any
	}
	fileGroups := make([]fileGroup, 0, len(groups))
	for src, qs := range groups {
		fileGroups = append(fileGroups, fileGroup{src, qs})
	}

	usedNames := make(map[string]int)
	var mu sync.Mutex

	numWorkers := min(runtime.NumCPU(), len(fileGroups))

	workCh := make(chan fileGroup, len(fileGroups))
	errCh := make(chan error, len(fileGroups))
	var wg sync.WaitGroup

	for i := 0; i < numWorkers; i++ {
		wg.Go(func() {
			for fg := range workCh {
				errCh <- p.processFile(fg.sourceFile, fg.queries, fullRegen, genFunc, &mu, usedNames)
			}
		})
	}
	for _, fg := range fileGroups {
		workCh <- fg
	}
	close(workCh)
	go func() { wg.Wait(); close(errCh) }()
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *IncrementalPipeline) processFile(sourceFile string, fileQueries []any, fullRegen bool, genFunc func(string, []any, bool) (string, error), mu *sync.Mutex, usedNames map[string]int) error {
	queryFile := filepath.Join(p.QueriesDir, sourceFile+".sql")
	currentHash, _ := ComputeFileChecksum(queryFile)

	// The skip decision must consider this pipeline's own output file so a
	// shared cache entry from another generator cannot suppress generation.
	primaryOutput := filepath.Join(p.OutDir, strings.TrimSuffix(sourceFile, ".sql")+p.Extension)
	if !ShouldRegenerateFileForOutput(p.Cache, queryFile, currentHash, fullRegen, primaryOutput) {
		PrintSkipMessage(sourceFile, p.Extension)
		return nil
	}
	PrintGenerateMessage(sourceFile, p.Extension)

	code, err := genFunc(sourceFile, fileQueries, fullRegen)
	if err != nil {
		return fmt.Errorf("generating %s: %w", sourceFile, err)
	}

	baseName := strings.TrimSuffix(sourceFile, ".sql")

	mu.Lock()
	outputFile := baseName + p.Extension
	if count, exists := usedNames[baseName]; exists {
		usedNames[baseName] = count + 1
		outputFile = fmt.Sprintf("%s_%d%s", baseName, count+1, p.Extension)
	} else {
		usedNames[baseName] = 1
	}
	mu.Unlock()

	path := filepath.Join(p.OutDir, outputFile)
	if err := os.WriteFile(path, []byte(code), 0644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	// Extract table dependencies from queries
	tableDeps := make([]string, 0)
	for _, q := range fileQueries {
		if q, ok := q.(interface{ GetSQL() string }); ok {
			if t := extractTableFromSQL(q.GetSQL()); t != "" {
				tableDeps = append(tableDeps, t)
			}
		}
	}

	UpdateCacheForFile(p.Cache, queryFile, currentHash, tableDeps, path)
	return nil
}

// extractTableFromSQL extracts a table name from SQL.
func extractTableFromSQL(sql string) string {
	// Simple extraction: look for FROM/JOIN/INTO/UPDATE keywords
	upper := strings.ToUpper(strings.TrimSpace(sql))
	for _, prefix := range []string{"INSERT INTO ", "UPDATE ", "FROM "} {
		if _, after, ok := strings.Cut(upper, prefix); ok {
			rest := strings.TrimSpace(after)
			if before, _, ok := strings.Cut(rest, " "); ok {
				return strings.Trim(before, `"'`)
			}
			return strings.Trim(rest, `"'`)
		}
	}
	return ""
}
