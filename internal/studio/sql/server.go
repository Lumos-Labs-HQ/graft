package sql

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/branch"
	"github.com/Lumos-Labs-HQ/flash/internal/config"
	"github.com/Lumos-Labs-HQ/flash/internal/database"
	"github.com/Lumos-Labs-HQ/flash/internal/studio/common"
)

type Server struct {
	*common.BaseServer
	service *Service
}

func NewServer(cfg *config.Config, port int, host, authToken string) *Server {
	dbURL, err := cfg.GetDatabaseURL()
	if err != nil {
		panic(fmt.Sprintf("Failed to get database URL: %v", err))
	}

	adapter := database.NewAdapter(cfg.Database.Provider)
	if err := adapter.Connect(context.Background(), dbURL); err != nil {
		panic(fmt.Sprintf("Failed to connect to database: %v", err))
	}

	if cfg.Database.URLEnv != "STUDIO_DB_URL" && cfg.MigrationsPath != "" {
		ctx := context.Background()
		branchMgr := branch.NewMetadataManager(cfg.MigrationsPath)
		if store, err := branchMgr.Load(); err == nil {
			if currentBranch := store.GetBranch(store.Current); currentBranch != nil {
				if cfg.Database.Provider == "postgresql" || cfg.Database.Provider == "postgres" {
					query := fmt.Sprintf("SET search_path TO %s, public", currentBranch.Schema)
					_, _ = adapter.ExecuteQuery(ctx, query)
					fmt.Printf("Studio using schema: %s (branch: %s)\n", currentBranch.Schema, currentBranch.Name)
				}
			}
		}
	}

	mux := http.NewServeMux()

	server := &Server{
		BaseServer: &common.BaseServer{
			Mux:       mux,
			Tmpl:      common.ParseTemplates(TemplatesFS),
			Port:      port,
			Host:      host,
			AuthToken: authToken,
			Name:      "Studio",
		},
		service: NewService(adapter, cfg),
	}
	server.setupRoutes()
	return server
}

func (s *Server) setupRoutes() {
	common.SetupStaticFS(s.Mux, StaticFS)

	s.Mux.HandleFunc("GET /{$}", s.handleIndex)
	s.Mux.HandleFunc("GET /schema", s.handleSchema)
	s.Mux.HandleFunc("GET /sql", s.handleSQL)
	s.Mux.HandleFunc("GET /metrics", s.handleMetrics)

	s.Mux.HandleFunc("GET /api/tables", s.handleGetTables)
	s.Mux.HandleFunc("GET /api/tables/{name}", s.handleGetTableData)
	s.Mux.HandleFunc("GET /api/schema", s.handleGetSchema)
	s.Mux.HandleFunc("POST /api/tables/{name}/save", s.handleSaveChanges)
	s.Mux.HandleFunc("POST /api/tables/{name}/add", s.handleAddRow)
	s.Mux.HandleFunc("POST /api/tables/{name}/delete", s.handleDeleteRows)
	s.Mux.HandleFunc("DELETE /api/tables/{name}/rows/{id}", s.handleDeleteRow)
	s.Mux.HandleFunc("POST /api/sql", s.handleExecuteSQL)

	// Schema Editor API
	s.Mux.HandleFunc("POST /api/schema/preview", s.handlePreviewSchemaChange)
	s.Mux.HandleFunc("POST /api/schema/apply", s.handleApplySchemaChange)
	s.Mux.HandleFunc("GET /api/config/check", s.handleCheckConfig)
	s.Mux.HandleFunc("PUT /api/tables/{name}/rows/{id}", s.handleUpdateRow)
	s.Mux.HandleFunc("POST /api/tables/{name}/rows", s.handleInsertRow)

	// Branch API
	s.Mux.HandleFunc("GET /api/branches", s.handleGetBranches)
	s.Mux.HandleFunc("POST /api/branches/switch", s.handleSwitchBranch)

	// Editor hints API (cached on client-side)
	s.Mux.HandleFunc("GET /api/editor/hints", s.handleGetEditorHints)

	// Metrics API
	s.Mux.HandleFunc("GET /api/metrics", s.handleGetMetrics)

	// Export/Import API
	s.Mux.HandleFunc("GET /api/export/{type}", s.handleExport)
	s.Mux.HandleFunc("POST /api/import", s.handleImport)
}

// UI Handlers
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.Render(w, "index.html", common.Map{"Title": "FlashORM Studio"})
}

func (s *Server) handleSchema(w http.ResponseWriter, r *http.Request) {
	s.Render(w, "schema.html", common.Map{"Title": "FlashORM Studio"})
}

func (s *Server) handleSQL(w http.ResponseWriter, r *http.Request) {
	s.Render(w, "sql.html", common.Map{"Title": "SQL Editor - FlashORM Studio"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	s.Render(w, "metrics.html", common.Map{"Title": "Metrics - FlashORM Studio"})
}

// API Handlers
func (s *Server) handleGetTables(w http.ResponseWriter, r *http.Request) {
	tables, err := s.service.GetTables()
	if err != nil {
		log.Printf("ERROR handleGetTables: %v", err)
		common.JSONError(w, http.StatusInternalServerError, sanitizeError(err))
		return
	}
	common.JSON(w, tables)
}

func (s *Server) handleGetTableData(w http.ResponseWriter, r *http.Request) {
	tableName := r.PathValue("name")
	page, _ := strconv.Atoi(common.Query(r, "page", "1"))
	limit, _ := strconv.Atoi(common.Query(r, "limit", "50"))

	// Parse filters from query parameter (JSON encoded)
	var filters []common.Filter
	if filtersJSON := r.URL.Query().Get("filters"); filtersJSON != "" {
		if err := json.Unmarshal([]byte(filtersJSON), &filters); err != nil {
			common.JSONError(w, http.StatusBadRequest, "Invalid filters format")
			return
		}
	}

	data, err := s.service.GetTableDataFiltered(tableName, page, limit, filters)
	if err != nil {
		log.Printf("ERROR handleGetTableData: %v", err)
		common.JSONError(w, http.StatusInternalServerError, sanitizeError(err))
		return
	}
	common.JSON(w, data)
}

func (s *Server) handleGetSchema(w http.ResponseWriter, r *http.Request) {
	schema, err := s.service.GetSchemaVisualization()
	if err != nil {
		log.Printf("ERROR handleGetSchema: %v", err)
		common.JSONError(w, http.StatusInternalServerError, sanitizeError(err))
		return
	}
	common.JSON(w, schema)
}

func (s *Server) handleSaveChanges(w http.ResponseWriter, r *http.Request) {
	tableName := r.PathValue("name")

	var req common.SaveRequest
	if err := common.ParseJSON(r, &req); err != nil {
		common.JSONError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	if err := s.service.SaveChanges(tableName, req.Changes); err != nil {
		log.Printf("ERROR handleSaveChanges: %v", err)
		msg := err.Error()
		if s := sanitizeInfraError(err); s != "" {
			msg = s
		}
		common.JSONError(w, http.StatusInternalServerError, msg)
		return
	}
	common.JSONMessage(w, "Changes saved successfully")
}

func (s *Server) handleAddRow(w http.ResponseWriter, r *http.Request) {
	tableName := r.PathValue("name")

	var req common.AddRowRequest
	if err := common.ParseJSON(r, &req); err != nil {
		common.JSONError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	if err := s.service.AddRow(tableName, req.Data); err != nil {
		log.Printf("ERROR handleAddRow: %v", err)
		common.JSONError(w, http.StatusInternalServerError, sanitizeError(err))
		return
	}
	common.JSONMessage(w, "Row added successfully")
}

func (s *Server) handleDeleteRow(w http.ResponseWriter, r *http.Request) {
	tableName := r.PathValue("name")
	rowID := r.PathValue("id")

	if err := s.service.DeleteRow(tableName, rowID); err != nil {
		log.Printf("ERROR handleDeleteRow: %v", err)
		common.JSONError(w, http.StatusInternalServerError, sanitizeError(err))
		return
	}
	common.JSONMessage(w, "Row deleted successfully")
}

func (s *Server) handleDeleteRows(w http.ResponseWriter, r *http.Request) {
	tableName := r.PathValue("name")

	var req struct {
		RowIDs []string `json:"row_ids"`
	}
	if err := common.ParseJSON(r, &req); err != nil {
		common.JSONError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	if err := s.service.DeleteRows(tableName, req.RowIDs); err != nil {
		log.Printf("ERROR handleDeleteRows: %v", err)
		common.JSONError(w, http.StatusInternalServerError, sanitizeError(err))
		return
	}
	common.JSONMessage(w, fmt.Sprintf("Deleted %d row(s) successfully", len(req.RowIDs)))
}

func (s *Server) handleExecuteSQL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}
	if err := common.ParseJSON(r, &req); err != nil {
		common.JSONError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	data, err := s.service.ExecuteSQL(req.Query)
	if err != nil {
		log.Printf("ERROR handleExecuteSQL: %v", err)
		status := classifySQLError(err)
		// Always show the real error for SQL execution — it's a user-visible query error,
		// not a server infrastructure error. Only sanitize true infrastructure errors.
		msg := err.Error()
		if s := sanitizeInfraError(err); s != "" {
			msg = s
		}
		common.JSONError(w, status, msg)
		return
	}
	common.JSON(w, data)
}

// sanitizeError returns a generic error message for the client.
// The original error should be logged server-side before calling this.
func sanitizeError(err error) string {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "connection") || strings.Contains(msg, "connect") {
		return "database connection error"
	}
	if strings.Contains(msg, "timeout") {
		return "request timed out"
	}
	if strings.Contains(msg, "permission") || strings.Contains(msg, "access denied") {
		return "permission denied"
	}
	return "internal error"
}

// sanitizeInfraError returns a generic message only for true infrastructure errors
// (connection, timeout, permission). Returns "" for normal SQL errors so callers
// can show the real message to the user.
func sanitizeInfraError(err error) string {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "connection") || strings.Contains(msg, "connect") {
		return "database connection error"
	}
	if strings.Contains(msg, "timeout") {
		return "request timed out"
	}
	if strings.Contains(msg, "permission") || strings.Contains(msg, "access denied") {
		return "permission denied"
	}
	return ""
}

// classifySQLError returns an appropriate HTTP status code for a SQL error.
// Syntax errors and constraint violations return 400 Bad Request.
// Connection and internal errors return 500 Internal Server Error.
func classifySQLError(err error) int {
	msg := strings.ToLower(err.Error())
	syntaxKeywords := []string{
		"syntax error", "unrecognized token", "near", "unexpected",
		"invalid", "parse error", "syntax", "incorrect syntax",
		"you have an error in your sql syntax",
	}
	for _, kw := range syntaxKeywords {
		if strings.Contains(msg, kw) {
			return http.StatusBadRequest
		}
	}
	// Constraint violations
	constraintKeywords := []string{
		"constraint", "foreign key", "unique constraint", "not null",
		"check constraint", "violates",
	}
	for _, kw := range constraintKeywords {
		if strings.Contains(msg, kw) {
			return http.StatusBadRequest
		}
	}
	return http.StatusInternalServerError
}

func (s *Server) handleUpdateRow(w http.ResponseWriter, r *http.Request) {
	table := r.PathValue("name")
	id := r.PathValue("id")

	var data map[string]any
	if err := common.ParseJSON(r, &data); err != nil {
		common.JSONError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	if err := s.service.UpdateRow(table, id, data); err != nil {
		log.Printf("ERROR handleUpdateRow: %v", err)
		common.JSONError(w, http.StatusInternalServerError, sanitizeError(err))
		return
	}
	common.JSONMap(w, common.Map{"success": true})
}

func (s *Server) handleInsertRow(w http.ResponseWriter, r *http.Request) {
	table := r.PathValue("name")

	var data map[string]any
	if err := common.ParseJSON(r, &data); err != nil {
		common.JSONError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	if err := s.service.InsertRow(table, data); err != nil {
		log.Printf("ERROR handleInsertRow: %v", err)
		common.JSONError(w, http.StatusInternalServerError, sanitizeError(err))
		return
	}
	common.JSONMap(w, common.Map{"success": true})
}

func (s *Server) handleGetEditorHints(w http.ResponseWriter, r *http.Request) {
	hints, err := s.service.GetEditorHints()
	if err != nil {
		log.Printf("ERROR handleGetEditorHints: %v", err)
		common.JSONError(w, http.StatusInternalServerError, sanitizeError(err))
		return
	}
	common.JSON(w, hints)
}

func (s *Server) handleGetMetrics(w http.ResponseWriter, r *http.Request) {
	metrics, err := s.service.GetMetrics(r.Context())
	if err != nil {
		log.Printf("ERROR handleGetMetrics: %v", err)
		common.JSONError(w, http.StatusInternalServerError, sanitizeError(err))
		return
	}
	common.JSON(w, metrics)
}
