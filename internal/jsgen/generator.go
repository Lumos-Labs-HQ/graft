package jsgen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/cachegen"
	"github.com/Lumos-Labs-HQ/flash/internal/config"
	"github.com/Lumos-Labs-HQ/flash/internal/gencommon"
	"github.com/Lumos-Labs-HQ/flash/internal/parser"
	"github.com/Lumos-Labs-HQ/flash/internal/utils"
)

type Generator struct {
	Config       *config.Config
	schema       *parser.Schema
	schemaParser *parser.SchemaParser
	queryParser  *parser.QueryParser
	cache        *gencommon.GenerationCache
}

func New(cfg *config.Config) *Generator {
	return &Generator{
		Config:       cfg,
		schemaParser: parser.NewSchemaParser(cfg),
		queryParser:  parser.NewQueryParser(cfg),
		cache:        gencommon.NewGenerationCacheWithDir(cfg.Gen.JS.Out),
	}
}

func (g *Generator) Generate() error {
	if err := os.MkdirAll(g.Config.Gen.JS.Out, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	schema, err := g.schemaParser.Parse()
	if err != nil {
		return fmt.Errorf("failed to parse schema: %w", err)
	}
	g.schema = schema

	// Compute checksums for incremental generation
	schemaHash, _ := g.cache.ComputeSchemaChecksum(g.Config.SchemaDir)
	configHash := fmt.Sprintf("%x", []byte(fmt.Sprintf("%s|%s|%s", g.Config.Database.Provider, g.Config.Gen.JS.Out, g.Config.Gen.JS.Driver)))
	fullRegen := g.Config.ForceRegen || g.cache.ShouldRegenerateAll(schemaHash, configHash)

	queries, err := g.queryParser.Parse(schema)
	if err != nil {
		return fmt.Errorf("failed to parse queries: %w", err)
	}

	// Remove cache entries and generated files for query sources that no
	// longer exist (deleted or renamed .sql/.cql files), mirroring gogen.
	pruned := g.cache.PruneStaleQueryEntries(g.Config.Queries)
	for _, source := range pruned {
		base := strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
		orphan := filepath.Join(g.Config.Gen.JS.Out, base+".js")
		if err := os.Remove(orphan); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove orphaned output %s: %w", orphan, err)
		}
	}

	if err := g.generateQueriesIncremental(queries, fullRegen); err != nil {
		return err
	}

	if err := g.generateDatabase(queries); err != nil {
		return err
	}
	if err := g.generateMigrations(); err != nil {
		return err
	}

	if err := g.generateTypeScriptDeclarations(schema, queries); err != nil {
		return err
	}

	// Generate cache layer if enabled
	if g.Config.Cache.Enabled {
		if err := g.generateCacheFiles(queries); err != nil {
			return err
		}
	}

	// Save cache for incremental generation
	g.cache.UpdateSchemaChecksum(schemaHash)
	g.cache.UpdateConfigChecksum(configHash)
	g.cache.MarkGeneration()
	_ = g.cache.Save()

	return nil
}

func (g *Generator) generateOptimizedQueryMethod(w *strings.Builder, query *parser.Query) {
	methodName := utils.Uncapitalize(query.Name)
	sql := g.convertSQL(query.SQL)
	sql = strings.ReplaceAll(sql, "`", "\\`")

	paramNames := make([]string, len(query.Params))
	for i, param := range query.Params {
		paramName := param.Name
		if paramName == "" {
			paramName = fmt.Sprintf("p%d", i+1)
		}
		paramNames[i] = paramName
	}

	hasColumns := len(query.Columns) > 0
	isSingleColumn := len(query.Columns) == 1 && query.Columns[0].Name != "*"
	isHotQuery := isSingleColumn && query.Cmd == ":one" && len(query.Params) <= 2

	driver := g.Config.Gen.JS.Driver
	isAsync := true
	if driver == "better-sqlite3" || driver == "bun:sqlite" {
		isAsync = false
	}

	useStructParams := len(paramNames) > 2
	var fullParams string
	if useStructParams {
		fullParams = "args"
	} else {
		fullParams = strings.Join(paramNames, ", ")
	}

	if isAsync {
		w.WriteString(fmt.Sprintf("  async %s(%s) {\n", methodName, fullParams))
	} else {
		w.WriteString(fmt.Sprintf("  %s(%s) {\n", methodName, fullParams))
	}

	// Destructure args object when using struct params
	if useStructParams {
		w.WriteString(fmt.Sprintf("    const { %s } = args;\n", strings.Join(paramNames, ", ")))
	}

	// postgres driver (porsager/postgres) uses tagged template literals
	if driver == "postgres" {
		g.generatePostgresDriverExecution(w, sql, paramNames, hasColumns, query.Cmd, isSingleColumn, query.Columns)
		w.WriteString("  }\n\n")
		return
	}

	w.WriteString(fmt.Sprintf("    let stmt = this._stmts.get('%s');\n", methodName))
	w.WriteString("    if (!stmt) {\n")

	provider := g.Config.Database.Provider
	useNamedStmt := (provider == "" || provider == "postgresql" || provider == "postgres") &&
		(isHotQuery || len(query.Params) == 0)

	if useNamedStmt {
		w.WriteString(fmt.Sprintf("      stmt = { name: '%s', text: `%s` };\n", methodName, sql))
	} else {
		w.WriteString(fmt.Sprintf("      stmt = `%s`;\n", sql))
	}

	w.WriteString(fmt.Sprintf("      this._stmts.set('%s', stmt);\n", methodName))
	w.WriteString("    }\n")

	switch provider {
	case "sqlite", "sqlite3":
		g.generateSQLiteExecution(w, paramNames, hasColumns, query.Cmd, isSingleColumn, query.Columns)
	case "mysql":
		if driver == "mysql2" {
			g.generateMySQL2Execution(w, paramNames, hasColumns, query.Cmd, isSingleColumn, query.Columns)
		} else {
			g.generateMySQLExecution(w, paramNames, hasColumns, query.Cmd, isSingleColumn, query.Columns)
		}
	case "scylla", "scylladb", "cassandra":
		g.generateScyllaExecution(w, paramNames, hasColumns, query.Cmd, isSingleColumn, query.Columns)
	default:
		g.generatePostgreSQLExecution(w, paramNames, hasColumns, query.Cmd, isSingleColumn, query.Columns, isHotQuery)
	}

	w.WriteString("  }\n\n")
}

func (g *Generator) generateScyllaExecution(w *strings.Builder, paramNames []string, hasColumns bool, cmd string, isSingleColumn bool, columns []*parser.QueryColumn) {
	if len(paramNames) > 0 {
		w.WriteString("    const r = await this.db.execute(stmt, [" + strings.Join(paramNames, ", ") + "], { prepare: true });\n")
	} else {
		w.WriteString("    const r = await this.db.execute(stmt, [], { prepare: true });\n")
	}
	if hasColumns {
		if cmd == ":one" {
			w.WriteString("    return r.first() || null;\n")
		} else {
			w.WriteString("    return r.rows;\n")
		}
	} else {
		w.WriteString("    return r;\n")
	}
}

func (g *Generator) generatePostgreSQLExecution(w *strings.Builder, paramNames []string, hasColumns bool, cmd string, isSingleColumn bool, columns []*parser.QueryColumn, isHotQuery bool) {
	if len(paramNames) > 0 {
		if isHotQuery {
			w.WriteString("    stmt.values = [" + strings.Join(paramNames, ", ") + "];\n")
			w.WriteString("    const r = await this.db.query(stmt);\n")
		} else {
			w.WriteString("    const r = await this.db.query(stmt, [" + strings.Join(paramNames, ", ") + "]);\n")
		}
	} else {
		if isHotQuery {
			w.WriteString("    stmt.values = [];\n")
			w.WriteString("    const r = await this.db.query(stmt);\n")
		} else {
			w.WriteString("    const r = await this.db.query(stmt);\n")
		}
	}

	if hasColumns {
		if cmd == ":one" {
			if isSingleColumn && len(columns) > 0 {
				colName := columns[0].Name
				if strings.Contains(colName, "(") || colName == "*" {
					w.WriteString("    return r.rows[0] ? Object.values(r.rows[0])[0] : null;\n")
				} else {
					w.WriteString(fmt.Sprintf("    return r.rows[0] ? r.rows[0].%s : null;\n", colName))
				}
			} else {
				w.WriteString("    return r.rows[0] || null;\n")
			}
		} else {
			if isSingleColumn && len(columns) > 0 {
				colName := columns[0].Name
				if strings.Contains(colName, "(") || colName == "*" {
					w.WriteString("    return r.rows.map(row => Object.values(row)[0]);\n")
				} else {
					w.WriteString(fmt.Sprintf("    return r.rows.map(row => row.%s);\n", colName))
				}
				w.WriteString("    return r.rows;\n")
			}
		}
	} else {
		switch cmd {
		case ":one":
			w.WriteString("    return r.rows[0] || null;\n")
		case ":many":
			w.WriteString("    return r.rows;\n")
		default:
			w.WriteString("    return r.rowCount;\n")
		}
	}
}

func (g *Generator) generateMySQLExecution(w *strings.Builder, paramNames []string, hasColumns bool, cmd string, isSingleColumn bool, columns []*parser.QueryColumn) {
	// The 'execute' method automatically prepares and caches statements
	w.WriteString("    const sql = typeof stmt === 'string' ? stmt : stmt.text;\n")
	if len(paramNames) > 0 {
		w.WriteString("    const r = await this.db.execute(sql, [" + strings.Join(paramNames, ", ") + "]);\n")
	} else {
		w.WriteString("    const r = await this.db.execute(sql);\n")
	}

	if hasColumns {
		if cmd == ":one" {
			if isSingleColumn && len(columns) > 0 {
				w.WriteString(fmt.Sprintf("    return r[0][0] ? r[0][0].%s : null;\n", columns[0].Name))
			} else {
				w.WriteString("    return r[0][0] || null;\n")
			}
		} else {
			if isSingleColumn && len(columns) > 0 {
				w.WriteString(fmt.Sprintf("    return r[0].map(row => row.%s);\n", columns[0].Name))
			} else {
				w.WriteString("    return r[0];\n")
			}
		}
	} else {
		w.WriteString("    return r[0].affectedRows;\n")
	}
}

func (g *Generator) generateMySQL2Execution(w *strings.Builder, paramNames []string, hasColumns bool, cmd string, isSingleColumn bool, columns []*parser.QueryColumn) {
	w.WriteString("    const sql = typeof stmt === 'string' ? stmt : stmt.text;\n")
	if len(paramNames) > 0 {
		w.WriteString("    const [rows] = await this.db.query(sql, [" + strings.Join(paramNames, ", ") + "]);\n")
	} else {
		w.WriteString("    const [rows] = await this.db.query(sql);\n")
	}

	if hasColumns {
		if cmd == ":one" {
			if isSingleColumn && len(columns) > 0 {
				w.WriteString(func() string {
					n := columns[0].Name
					if strings.Contains(n, "(") || n == "*" {
						return "    return rows[0] ? Object.values(rows[0])[0] : null;\n"
					}
					return fmt.Sprintf("    return rows[0] ? rows[0].%s : null;\n", n)
				}())
			} else {
				w.WriteString("    return rows[0] || null;\n")
			}
		} else {
			if isSingleColumn && len(columns) > 0 {
				w.WriteString(func() string {
					n := columns[0].Name
					if strings.Contains(n, "(") || n == "*" {
						return "    return rows.map(row => Object.values(row)[0]);\n"
					}
					return fmt.Sprintf("    return rows.map(row => row.%s);\n", n)
				}())
			} else {
				w.WriteString("    return rows;\n")
			}
		}
	} else {
		w.WriteString("    return rows.affectedRows;\n")
	}
}

func (g *Generator) generatePostgresDriverExecution(w *strings.Builder, sql string, paramNames []string, hasColumns bool, cmd string, isSingleColumn bool, columns []*parser.QueryColumn) {
	// Convert $N placeholders to ${paramName} for tagged template literals
	templateSQL := sql
	for i, name := range paramNames {
		templateSQL = strings.ReplaceAll(templateSQL, fmt.Sprintf("$%d", i+1), "${"+name+"}")
	}

	w.WriteString(fmt.Sprintf("    const r = await this.db`%s`;\n", templateSQL))

	if hasColumns {
		if cmd == ":one" {
			if isSingleColumn && len(columns) > 0 {
				w.WriteString(fmt.Sprintf("    return r[0] ? r[0].%s : null;\n", columns[0].Name))
			} else {
				w.WriteString("    return r[0] || null;\n")
			}
		} else {
			if isSingleColumn && len(columns) > 0 {
				w.WriteString(fmt.Sprintf("    return r.map(row => row.%s);\n", columns[0].Name))
			} else {
				w.WriteString("    return r;\n")
			}
		}
	} else {
		switch cmd {
		case ":one":
			w.WriteString("    return r[0] || null;\n")
		case ":many":
			w.WriteString("    return r;\n")
		default:
			w.WriteString("    return r.count;\n")
		}
	}
}

func (g *Generator) generateSQLiteExecution(w *strings.Builder, paramNames []string, hasColumns bool, cmd string, isSingleColumn bool, columns []*parser.QueryColumn) {
	w.WriteString("    const sql = typeof stmt === 'string' ? stmt : stmt.text;\n")
	w.WriteString("    const prepared = this.db.prepare(sql);\n")

	if hasColumns {
		if cmd == ":one" {
			if len(paramNames) > 0 {
				if isSingleColumn && len(columns) > 0 {
					w.WriteString("    const row = prepared.get(" + strings.Join(paramNames, ", ") + ");\n")
					w.WriteString(fmt.Sprintf("    return row ? row.%s : null;\n", columns[0].Name))
				} else {
					w.WriteString("    return prepared.get(" + strings.Join(paramNames, ", ") + ") || null;\n")
				}
			} else {
				if isSingleColumn && len(columns) > 0 {
					w.WriteString("    const row = prepared.get();\n")
					w.WriteString(fmt.Sprintf("    return row ? row.%s : null;\n", columns[0].Name))
				} else {
					w.WriteString("    return prepared.get() || null;\n")
				}
			}
		} else {
			if len(paramNames) > 0 {
				if isSingleColumn && len(columns) > 0 {
					w.WriteString("    const rows = prepared.all(" + strings.Join(paramNames, ", ") + ");\n")
					w.WriteString(func() string {
						n := columns[0].Name
						if strings.Contains(n, "(") || n == "*" {
							return "    return rows.map(row => Object.values(row)[0]);\n"
						}
						return fmt.Sprintf("    return rows.map(row => row.%s);\n", n)
					}())
				} else {
					w.WriteString("    return prepared.all(" + strings.Join(paramNames, ", ") + ");\n")
				}
			} else {
				if isSingleColumn && len(columns) > 0 {
					w.WriteString("    const rows = prepared.all();\n")
					w.WriteString(func() string {
						n := columns[0].Name
						if strings.Contains(n, "(") || n == "*" {
							return "    return rows.map(row => Object.values(row)[0]);\n"
						}
						return fmt.Sprintf("    return rows.map(row => row.%s);\n", n)
					}())
				} else {
					w.WriteString("    return prepared.all();\n")
				}
			}
		}
	} else {
		if len(paramNames) > 0 {
			w.WriteString("    const info = prepared.run(" + strings.Join(paramNames, ", ") + ");\n")
		} else {
			w.WriteString("    const info = prepared.run();\n")
		}
		w.WriteString("    return info.changes;\n")
	}
}

func (g *Generator) getReturnType(query *parser.Query) string {
	if len(query.Columns) == 1 && query.Columns[0].Name != "*" {
		colName := strings.ToLower(query.Columns[0].Name)
		if colName == "count" || colName == "sum" || colName == "avg" ||
			colName == "max" || colName == "min" || colName == "total" ||
			strings.HasSuffix(colName, "_count") || strings.HasSuffix(colName, "_sum") ||
			strings.HasPrefix(colName, "avg_") || strings.HasPrefix(colName, "total_") {
			return "number"
		}

		if colName == "exists" || strings.HasPrefix(colName, "is") || strings.HasPrefix(colName, "has") {
			return "boolean"
		}

		if query.Columns[0].Table != "" && g.schema != nil {
			for _, table := range g.schema.Tables {
				if strings.EqualFold(table.Name, query.Columns[0].Table) {
					for _, col := range table.Columns {
						if strings.EqualFold(col.Name, query.Columns[0].Name) {
							return g.mapSQLTypeToJS(col.Type)
						}
					}
				}
			}
		}

		// Fallback: map the SQL type directly (handles COUNT(*), expressions, etc.)
		colType := query.Columns[0].Type
		if colType != "" {
			if jsType := g.mapSQLTypeToJS(colType); jsType != "string" {
				return jsType
			}
		}
		return "any"
	}

	if len(query.Columns) == 1 && query.Columns[0].Name == "*" && query.Columns[0].Table != "" {
		return utils.Capitalize(query.Columns[0].Table)
	}

	if len(query.Columns) > 1 {
		return "Object"
	}

	// Use shared utility to extract table name
	tableName := utils.ExtractTableName(query.SQL)
	if tableName != "" {
		return utils.Capitalize(tableName)
	}

	return "Object"
}

func (g *Generator) generateDatabase(queries []*parser.Query) error {
	// Get unique source files
	sourceFiles := make(map[string]bool)
	filesList := []string{}
	for _, query := range queries {
		sourceFile := query.SourceFile
		if sourceFile == "" {
			sourceFile = "queries"
		}
		baseName := strings.TrimSuffix(sourceFile, ".sql")
		if !sourceFiles[baseName] {
			sourceFiles[baseName] = true
			filesList = append(filesList, baseName)
		}
	}

	var w strings.Builder
	w.Grow(512) // Pre-allocate for index file
	w.WriteString("// Code generated by FlashORM. DO NOT EDIT.\n")
	w.WriteString("const { FlashInitMigrate } = require('./migrations');\n")

	if len(filesList) == 1 {
		w.WriteString(fmt.Sprintf("const { Queries } = require('./%s');\n\n", filesList[0]))
	} else {
		for _, baseName := range filesList {
			w.WriteString(fmt.Sprintf("const { Queries: %sQueries } = require('./%s');\n",
				utils.Capitalize(baseName), baseName))
		}
		w.WriteString("\n")

		w.WriteString("// Combine all query classes into one\n")
		w.WriteString("class Queries {\n")
		w.WriteString("  constructor(db) {\n")
		w.WriteString("    this.db = db;\n")

		for i, baseName := range filesList {
			className := utils.Capitalize(baseName)
			instanceName := fmt.Sprintf("_%s", baseName)
			w.WriteString(fmt.Sprintf("    const %s = new %sQueries(db);\n", instanceName, className))

			w.WriteString(fmt.Sprintf("    Object.getOwnPropertyNames(Object.getPrototypeOf(%s)).forEach(name => {\n", instanceName))
			w.WriteString("      if (name !== 'constructor' && typeof " + instanceName + "[name] === 'function') {\n")
			w.WriteString("        this[name] = " + instanceName + "[name].bind(" + instanceName + ");\n")
			w.WriteString("      }\n")
			w.WriteString("    });\n")

			if i < len(filesList)-1 {
				w.WriteString("\n")
			}
		}

		w.WriteString("  }\n")
		w.WriteString("}\n\n")
	}

	w.WriteString("/**\n")
	w.WriteString(" * Create a new database client\n")
	provider := g.Config.Database.Provider
	if provider == "scylla" || provider == "scylladb" || provider == "cassandra" {
		w.WriteString(" * @param {Object} db - ScyllaDB/Cassandra client (from cassandra-driver)\n")
	} else {
		w.WriteString(" * @param {Object} db - Database connection (pg.Pool, postgres instance, mysql2.Pool, better-sqlite3 instance, or bun:sqlite instance)\n")
	}
	w.WriteString(" * @returns {Queries}\n")
	w.WriteString(" */\n")
	w.WriteString("function Newq(db) {\n")
	w.WriteString("  return new Queries(db);\n")
	w.WriteString("}\n\n")

	w.WriteString("module.exports = { Newq, Queries, FlashInitMigrate };\n")

	path := filepath.Join(g.Config.Gen.JS.Out, "index.js")
	return os.WriteFile(path, []byte(w.String()), 0644)
}

func (g *Generator) mapSQLTypeToJS(sqlType string) string {
	sqlTypeLower := strings.ToLower(sqlType)

	if strings.HasPrefix(sqlTypeLower, "enum(") {
		values := gencommon.ExtractEnumValues(sqlType)
		if len(values) > 0 {
			quotedValues := make([]string, len(values))
			for i, v := range values {
				quotedValues[i] = fmt.Sprintf("'%s'", v)
			}
			return strings.Join(quotedValues, " | ")
		}
	}

	if g.schema != nil {
		for _, enum := range g.schema.Enums {
			if strings.ToLower(enum.Name) == sqlTypeLower {
				quotedValues := make([]string, len(enum.Values))
				for i, v := range enum.Values {
					quotedValues[i] = fmt.Sprintf("'%s'", v)
				}
				return strings.Join(quotedValues, " | ")
			}
		}
	}

	// Check for array types (TEXT[], INTEGER[], etc.)
	if before, ok := strings.CutSuffix(sqlTypeLower, "[]"); ok {
		baseType := before
		return g.mapSQLTypeToJS(baseType) + "[]"
	}

	switch {
	case strings.Contains(sqlTypeLower, "int"), strings.Contains(sqlTypeLower, "serial"):
		return "number"
	case strings.Contains(sqlTypeLower, "numeric"), strings.Contains(sqlTypeLower, "decimal"), strings.Contains(sqlTypeLower, "float"), strings.Contains(sqlTypeLower, "double"), strings.Contains(sqlTypeLower, "real"):
		return "number"
	case strings.Contains(sqlTypeLower, "bool"):
		return "boolean"
	case strings.Contains(sqlTypeLower, "json"):
		return "Object"
	// CQL timeuuid/time must be checked BEFORE generic "time" — "timeuuid" contains "time"
	case sqlTypeLower == "uuid", sqlTypeLower == "timeuuid":
		return "string"
	case strings.Contains(sqlTypeLower, "timestamp"), strings.Contains(sqlTypeLower, "date"), strings.Contains(sqlTypeLower, "time"):
		return "Date"
	case sqlTypeLower == "interval":
		return "number" // interval as milliseconds
	case strings.Contains(sqlTypeLower, "bytea"), strings.Contains(sqlTypeLower, "blob"):
		return "Uint8Array"
	// ─── PostgreSQL Extension Types ───────────────────────────────────────
	case sqlTypeLower == "vector" || strings.HasPrefix(sqlTypeLower, "vector(") ||
		sqlTypeLower == "halfvec" || strings.HasPrefix(sqlTypeLower, "halfvec("):
		return "number[]" // pgvector
	case sqlTypeLower == "hstore":
		return "Record<string, string>"
	case sqlTypeLower == "tsvector", sqlTypeLower == "tsquery":
		return "string"
	case sqlTypeLower == "ltree", sqlTypeLower == "lquery", sqlTypeLower == "ltxtquery":
		return "string"
	case sqlTypeLower == "money":
		return "string"
	case sqlTypeLower == "xml":
		return "string"
	case sqlTypeLower == "pg_lsn":
		return "string"
	case sqlTypeLower == "bit" || strings.HasPrefix(sqlTypeLower, "bit(") ||
		sqlTypeLower == "varbit" || strings.HasPrefix(sqlTypeLower, "varbit(") ||
		strings.HasPrefix(sqlTypeLower, "bit varying"):
		return "string"
	case sqlTypeLower == "point", sqlTypeLower == "line", sqlTypeLower == "lseg",
		sqlTypeLower == "box", sqlTypeLower == "path", sqlTypeLower == "polygon", sqlTypeLower == "circle":
		return "string"
	case sqlTypeLower == "geometry" || strings.HasPrefix(sqlTypeLower, "geometry(") ||
		sqlTypeLower == "geography" || strings.HasPrefix(sqlTypeLower, "geography("):
		return "string" // PostGIS WKT/GeoJSON
	case sqlTypeLower == "cidr", sqlTypeLower == "macaddr", sqlTypeLower == "macaddr8":
		return "string"
	case strings.HasSuffix(sqlTypeLower, "range") || strings.HasSuffix(sqlTypeLower, "multirange"):
		return "string"
	// ClickHouse-specific types
	case sqlTypeLower == "uint8", sqlTypeLower == "uint16", sqlTypeLower == "uint32", sqlTypeLower == "uint64",
		sqlTypeLower == "int8", sqlTypeLower == "int16", sqlTypeLower == "int32", sqlTypeLower == "int64",
		sqlTypeLower == "float32", sqlTypeLower == "float64":
		return "number"
	case strings.HasPrefix(sqlTypeLower, "fixedstring"), sqlTypeLower == "string":
		return "string"
	case strings.HasPrefix(sqlTypeLower, "nullable("):
		inner := sqlTypeLower[9 : len(sqlTypeLower)-1]
		return g.mapSQLTypeToJS(inner) + " | null"
	case strings.HasPrefix(sqlTypeLower, "array("):
		inner := sqlTypeLower[6 : len(sqlTypeLower)-1]
		return g.mapSQLTypeToJS(inner) + "[]"
	// ScyllaDB/Cassandra CQL types (non-uuid)
	case sqlTypeLower == "inet":
		return "string"
	case sqlTypeLower == "varint", sqlTypeLower == "counter", sqlTypeLower == "duration":
		return "number"
	case strings.HasPrefix(sqlTypeLower, "frozen<"), strings.HasPrefix(sqlTypeLower, "list<"),
		strings.HasPrefix(sqlTypeLower, "set<"), strings.HasPrefix(sqlTypeLower, "map<"),
		strings.HasPrefix(sqlTypeLower, "tuple<"):
		return "any"
	default:
		return "string"
	}
}

func (g *Generator) convertSQL(sql string) string {
	switch g.Config.Database.Provider {
	case "mysql", "sqlite", "sqlite3":
		result := sql
		for i := 20; i >= 1; i-- {
			result = strings.ReplaceAll(result, fmt.Sprintf("$%d", i), "?")
		}
		return result
	default:
		return sql
	}
}

func (g *Generator) generateTypeScriptDeclarations(schema *parser.Schema, queries []*parser.Query) error {
	var w strings.Builder
	estimatedSize := 200 + (len(schema.Tables) * 200) + (len(queries) * 300)
	w.Grow(estimatedSize)
	w.WriteString("// Code generated by FlashORM. DO NOT EDIT.\n\n")

	// JSON type interfaces from @json annotations
	emittedJsonTypes := make(map[string]bool)
	for _, query := range queries {
		for _, jt := range query.JsonTypes {
			if emittedJsonTypes[jt.Name] {
				continue
			}
			emittedJsonTypes[jt.Name] = true
			w.WriteString(fmt.Sprintf("/** JSON type for column '%s'. Parse with: JSON.parse(row.%s) as %s */\n", jt.Column, jt.Column, jt.Name))
			w.WriteString(fmt.Sprintf("export interface %s {\n", jt.Name))
			for _, field := range jt.Fields {
				tsType := jsonFieldToTSType(field)
				w.WriteString(fmt.Sprintf("  %s: %s;\n", field.Name, tsType))
			}
			w.WriteString("}\n\n")
		}
	}

	// Table interfaces
	for _, table := range schema.Tables {
		structName := utils.Capitalize(table.Name)
		w.WriteString(fmt.Sprintf("export interface %s {\n", structName))
		for _, col := range table.Columns {
			jsType := g.mapSQLTypeToJS(col.Type)
			if col.Nullable {
				jsType += " | null"
			}
			w.WriteString(fmt.Sprintf("  %s: %s;\n", col.Name, jsType))
		}
		w.WriteString("}\n\n")
	}

	// Result interfaces for multi-column queries
	complexQueries := make(map[string]*parser.Query)
	for _, query := range queries {
		if len(query.Columns) > 1 && query.Cmd != ":exec" {
			complexQueries[query.Name] = query
		}
	}
	for queryName, query := range complexQueries {
		interfaceName := utils.Capitalize(queryName) + "Result"
		w.WriteString(fmt.Sprintf("export interface %s {\n", interfaceName))
		for _, col := range query.Columns {
			if col.Name == "*" {
				continue
			}
			jsType := g.inferColumnTypeFromSchema(col)
			if col.JsonDef != nil {
				jsType = col.JsonDef.Name + " | null"
			}
			w.WriteString(fmt.Sprintf("  %s: %s;\n", col.Name, jsType))
		}
		w.WriteString("}\n\n")
	}

	// Args interfaces for queries with >2 params
	seenArgs := make(map[string]bool)
	for _, query := range queries {
		if len(query.Params) <= 2 {
			continue
		}
		argsType := utils.Capitalize(query.Name) + "Params"
		if seenArgs[argsType] {
			continue
		}
		seenArgs[argsType] = true
		w.WriteString(fmt.Sprintf("export interface %s {\n", argsType))
		for i, param := range query.Params {
			paramName := param.Name
			if paramName == "" {
				paramName = fmt.Sprintf("p%d", i+1)
			}
			tsType := g.mapSQLTypeToJS(param.Type)
			if param.Nullable {
				w.WriteString(fmt.Sprintf("  %s?: %s | null;\n", paramName, tsType))
			} else {
				w.WriteString(fmt.Sprintf("  %s: %s;\n", paramName, tsType))
			}
		}
		w.WriteString("}\n\n")
	}

	// Queries class
	w.WriteString("export class Queries {\n")
	w.WriteString("  constructor(db: any);\n\n")

	isAsyncJS := g.Config.Gen.JS.Driver != "better-sqlite3" && g.Config.Gen.JS.Driver != "bun:sqlite"

	seenMethods := make(map[string]bool)
	for _, query := range queries {
		methodName := utils.Uncapitalize(query.Name)
		if seenMethods[methodName] {
			continue
		}
		seenMethods[methodName] = true

		// Build param declaration
		var paramDecl string
		if len(query.Params) > 2 {
			paramDecl = fmt.Sprintf("args: %s", utils.Capitalize(query.Name)+"Params")
		} else {
			parts := make([]string, len(query.Params))
			for i, param := range query.Params {
				paramName := param.Name
				if paramName == "" {
					paramName = fmt.Sprintf("p%d", i+1)
				}
				tsType := g.mapSQLTypeToJS(param.Type)
				if param.Nullable {
					parts[i] = fmt.Sprintf("%s: %s | null", paramName, tsType)
				} else {
					parts[i] = fmt.Sprintf("%s: %s", paramName, tsType)
				}
			}
			paramDecl = strings.Join(parts, ", ")
		}

		// Build return type
		returnType := g.getReturnType(query)
		if len(query.Columns) > 1 && query.Cmd != ":exec" {
			returnType = utils.Capitalize(query.Name) + "Result"
		}
		switch query.Cmd {
		case ":one":
			if isAsyncJS {
				returnType = fmt.Sprintf("Promise<%s | null>", returnType)
			} else {
				returnType = returnType + " | null"
			}
		case ":many":
			if isAsyncJS {
				returnType = fmt.Sprintf("Promise<%s[]>", returnType)
			} else {
				returnType = returnType + "[]"
			}
		default:
			if isAsyncJS {
				returnType = "Promise<number>"
			} else {
				returnType = "number"
			}
		}

		w.WriteString(fmt.Sprintf("  %s(%s): %s;\n", methodName, paramDecl, returnType))
	}

	w.WriteString("}\n\n")
	w.WriteString("export function Newq(db: any): Queries;\n")
	w.WriteString("export function FlashInitMigrate(db: any): Promise<void> | void;\n")

	path := filepath.Join(g.Config.Gen.JS.Out, "index.d.ts")
	return os.WriteFile(path, []byte(w.String()), 0644)
}

func (g *Generator) inferColumnTypeFromSchema(col *parser.QueryColumn) string {
	jsType := g.mapSQLTypeToJS(col.Type)
	if col.Nullable {
		jsType += " | null"
	}
	return jsType
}

// jsonFieldToTSType converts a JsonField type to its TypeScript representation.
func jsonFieldToTSType(field *parser.JsonField) string {
	var tsType string
	switch field.Type {
	case "string":
		tsType = "string"
	case "int", "float":
		tsType = "number"
	case "boolean":
		tsType = "boolean"
	case "string[]":
		tsType = "string[]"
	case "int[]", "float[]":
		tsType = "number[]"
	case "boolean[]":
		tsType = "boolean[]"
	case "any":
		tsType = "any"
	default:
		tsType = field.Type
	}
	if field.Nullable {
		tsType += " | null"
	}
	return tsType
}

func (g *Generator) generateCacheFiles(queries []*parser.Query) error {
	// Generate cache.js
	cacheCode := cachegen.GenerateTypeScriptCache(&g.Config.Cache)
	cachePath := filepath.Join(g.Config.Gen.JS.Out, "cache.js")
	if err := os.WriteFile(cachePath, []byte(cacheCode), 0644); err != nil {
		return fmt.Errorf("failed to write cache.js: %w", err)
	}

	// Resolve cache annotations
	caches := cachegen.ResolveCacheQueries(queries, &g.Config.Cache)

	// Validate dep references
	if warnings := cachegen.ValidateDeps(caches, queries); len(warnings) > 0 {
		for _, w := range warnings {
			fmt.Printf("⚠️  %s\n", w)
		}
	}

	// Generate cache_accessors.js
	accessorCode := cachegen.GenerateTypeScriptCacheAccessors(caches)
	if accessorCode == "" {
		accessorCode = "// Code generated by FlashORM. DO NOT EDIT.\n// No @cache annotations found.\n"
	}
	accessorPath := filepath.Join(g.Config.Gen.JS.Out, "cache_accessors.js")
	if err := os.WriteFile(accessorPath, []byte(accessorCode), 0644); err != nil {
		return fmt.Errorf("failed to write cache_accessors.js: %w", err)
	}

	// Generate cached_queries.js — cache-wrapped queries + auto-purge mutations
	cachedCode := g.generateCachedQueries(queries, caches)
	cachedPath := filepath.Join(g.Config.Gen.JS.Out, "cached_queries.js")
	if err := os.WriteFile(cachedPath, []byte(cachedCode), 0644); err != nil {
		return fmt.Errorf("failed to write cached_queries.js: %w", err)
	}

	return nil
}

func (g *Generator) generateCachedQueries(queries []*parser.Query, caches []*cachegen.CacheInfo) string {
	var w strings.Builder
	w.WriteString("// Code generated by FlashORM. DO NOT EDIT.\n\n")
	w.WriteString("const { cache } = require('./cache');\n\n")

	hasContent := false
	for _, q := range queries {
		if cachegen.IsCachedQuery(q) && !cachegen.IsMutationQuery(q) {
			g.writeJSCachedQuery(&w, q, caches)
			hasContent = true
		} else if cachegen.IsMutationQuery(q) {
			purges := cachegen.ResolveDependencyPurges(q.Name, caches, q.Params)
			if len(purges) > 0 {
				g.writeJSPurgingMutation(&w, q, purges)
				hasContent = true
			}
		}
	}

	if !hasContent {
		return "// Code generated by FlashORM. DO NOT EDIT.\n// No cached queries or auto-purge mutations.\n"
	}

	w.WriteString("module.exports = { ")
	first := true
	for _, q := range queries {
		if cachegen.IsCachedQuery(q) && !cachegen.IsMutationQuery(q) {
			if !first {
				w.WriteString(", ")
			}
			w.WriteString(q.Name + "Cached")
			first = false
		} else if cachegen.IsMutationQuery(q) {
			purges := cachegen.ResolveDependencyPurges(q.Name, caches, q.Params)
			if len(purges) > 0 {
				if !first {
					w.WriteString(", ")
				}
				w.WriteString(q.Name + "AndPurge")
				first = false
			}
		}
	}
	w.WriteString(" };\n")
	return w.String()
}

func (g *Generator) writeJSCachedQuery(w *strings.Builder, q *parser.Query, caches []*cachegen.CacheInfo) {
	cacheInfo := cachegen.GetCacheInfoForQuery(q.Name, caches)
	if cacheInfo == nil {
		return
	}
	ttlMs := cachegen.ParseTTL(cacheInfo.TTL) * 1000

	var params []string
	for _, p := range q.Params {
		params = append(params, p.Name)
	}
	paramStr := strings.Join(params, ", ")

	var keyExpr string
	if len(params) == 0 {
		keyExpr = fmt.Sprintf("`%s`", cacheInfo.CacheName)
	} else {
		keyExpr = fmt.Sprintf("`%s:${%s}`", cacheInfo.CacheName, strings.Join(params, "}:${"))
	}

	tagsLiteral := "[]"
	if len(cacheInfo.Tags) > 0 {
		quoted := make([]string, len(cacheInfo.Tags))
		for i, t := range cacheInfo.Tags {
			quoted[i] = fmt.Sprintf("'%s'", t)
		}
		tagsLiteral = fmt.Sprintf("[%s]", strings.Join(quoted, ", "))
	}

	w.WriteString(fmt.Sprintf("/**\n * %s (cached, TTL: %s)\n */\n", q.Name, cacheInfo.TTL))
	w.WriteString(fmt.Sprintf("async function %sCached(queries, %s) {\n", q.Name, paramStr))
	w.WriteString(fmt.Sprintf("  const key = %s;\n", keyExpr))
	w.WriteString("  const cached = await cache.get(key);\n")
	w.WriteString("  if (cached) return cached;\n\n")
	w.WriteString(fmt.Sprintf("  const result = await queries.%s(%s);\n", q.Name, paramStr))
	w.WriteString(fmt.Sprintf("  await cache.set(key, result, %d, %s);\n", ttlMs, tagsLiteral))
	w.WriteString("  return result;\n")
	w.WriteString("}\n\n")
}

func (g *Generator) writeJSPurgingMutation(w *strings.Builder, q *parser.Query, purges []cachegen.DependencyPurge) {
	var params []string
	for _, p := range q.Params {
		params = append(params, p.Name)
	}
	paramStr := strings.Join(params, ", ")

	w.WriteString(fmt.Sprintf("/**\n * %s (auto-purges dependent caches)\n */\n", q.Name))
	w.WriteString(fmt.Sprintf("async function %sAndPurge(queries, %s) {\n", q.Name, paramStr))
	w.WriteString(fmt.Sprintf("  const result = await queries.%s(%s);\n\n", q.Name, paramStr))
	w.WriteString("  // Auto-purge dependent caches\n")
	for _, purge := range purges {
		if purge.PurgePrefix {
			w.WriteString(fmt.Sprintf("  await cache.purgeByPrefix('%s:');\n", purge.CacheName))
		} else {
			w.WriteString(fmt.Sprintf("  await cache.del(`%s:${%s}`);\n", purge.CacheName, purge.KeyParam))
		}
	}
	w.WriteString("\n  return result;\n")
	w.WriteString("}\n\n")
}
