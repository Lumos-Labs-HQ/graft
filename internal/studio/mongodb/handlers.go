package mongodb

import (
	"encoding/json"
	"net/http"
	"strconv"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/Lumos-Labs-HQ/flash/internal/studio/common"
)

// Database Handlers
func (s *Server) handleGetDatabases(w http.ResponseWriter, r *http.Request) {
	databases, err := s.service.GetDatabases()
	common.Result(w, databases, err)
}

func (s *Server) handleSelectDatabase(w http.ResponseWriter, r *http.Request) {
	dbName := r.PathValue("name")
	if dbName == "" {
		common.JSONError(w, http.StatusBadRequest, "database name is required")
		return
	}

	common.ResultMessage(w, "Database switched successfully", s.service.SwitchDatabase(dbName))
}

func (s *Server) handleDropDatabase(w http.ResponseWriter, r *http.Request) {
	dbName := r.PathValue("name")
	if dbName == "" {
		common.JSONError(w, http.StatusBadRequest, "database name is required")
		return
	}

	common.ResultMessage(w, "Database dropped successfully", s.service.DropDatabase(dbName))
}

func (s *Server) handleCreateDatabase(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := common.ParseJSON(r, &req); err != nil {
		common.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		common.JSONError(w, http.StatusBadRequest, "database name is required")
		return
	}

	common.ResultMessage(w, "Database created successfully", s.service.CreateDatabase(req.Name))
}

// Collection Handlers
func (s *Server) handleGetCollections(w http.ResponseWriter, r *http.Request) {
	dbName := r.URL.Query().Get("database")
	if dbName == "" {
		common.JSONError(w, http.StatusBadRequest, "database parameter is required")
		return
	}

	collections, err := s.service.GetCollections(dbName)
	common.Result(w, collections, err)
}

func (s *Server) handleGetCollectionData(w http.ResponseWriter, r *http.Request) {
	dbName := r.URL.Query().Get("database")
	if dbName == "" {
		common.JSONError(w, http.StatusBadRequest, "database parameter is required")
		return
	}

	if err := s.service.SwitchDatabase(dbName); err != nil {
		common.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	name := r.PathValue("name")
	page, _ := strconv.Atoi(common.Query(r, "page", "1"))
	limit, _ := strconv.Atoi(common.Query(r, "limit", "50"))
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}

	result, err := s.service.GetDocuments(dbName, name, page, limit)
	common.Result(w, result, err)
}

func (s *Server) handleCreateCollection(w http.ResponseWriter, r *http.Request) {
	dbName := r.URL.Query().Get("database")
	if dbName != "" {
		if err := s.service.SwitchDatabase(dbName); err != nil {
			common.JSONError(w, http.StatusInternalServerError, "Failed to switch database: "+err.Error())
			return
		}
	}

	var req struct {
		Name    string         `json:"name"`
		Options map[string]any `json:"options"`
	}
	if err := common.ParseJSON(r, &req); err != nil {
		common.JSONError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	common.ResultMessage(w, "Collection created successfully", s.service.CreateCollection(req.Name, req.Options))
}

func (s *Server) handleDropCollection(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	dbName := r.URL.Query().Get("database")

	if dbName != "" {
		if err := s.service.SwitchDatabase(dbName); err != nil {
			common.JSONError(w, http.StatusInternalServerError, "Failed to switch database: "+err.Error())
			return
		}
	}

	common.ResultMessage(w, "Collection dropped successfully", s.service.DropCollection(name))
}

// Document Handlers
func (s *Server) handleGetDocuments(w http.ResponseWriter, r *http.Request) {
	dbName := r.URL.Query().Get("database")
	if dbName == "" {
		common.JSONError(w, http.StatusBadRequest, "database parameter is required")
		return
	}

	name := r.PathValue("name")
	page, _ := strconv.Atoi(common.Query(r, "page", "1"))
	limit, _ := strconv.Atoi(common.Query(r, "limit", "50"))
	filterStr := common.Query(r, "filter", "")
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}

	var filter bson.M
	if filterStr != "" {
		if err := json.Unmarshal([]byte(filterStr), &filter); err != nil {
			common.JSONError(w, http.StatusBadRequest, "Invalid filter JSON: "+err.Error())
			return
		}
	}

	result, err := s.service.GetDocumentsWithFilter(dbName, name, page, limit, filter)
	common.Result(w, result, err)
}

func (s *Server) handleInsertDocument(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	dbName := r.URL.Query().Get("database")

	if dbName != "" {
		if err := s.service.SwitchDatabase(dbName); err != nil {
			common.JSONError(w, http.StatusInternalServerError, "Failed to switch database: "+err.Error())
			return
		}
	}

	var document map[string]any
	if err := common.ParseJSON(r, &document); err != nil {
		common.JSONError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	id, err := s.service.InsertDocument(name, document)
	if err != nil {
		common.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	common.JSONMap(w, common.Map{"success": true, "message": "Document inserted successfully", "id": id})
}

func (s *Server) handleUpdateDocument(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	id := r.PathValue("id")
	dbName := r.URL.Query().Get("database")

	if dbName != "" {
		if err := s.service.SwitchDatabase(dbName); err != nil {
			common.JSONError(w, http.StatusInternalServerError, "Failed to switch database: "+err.Error())
			return
		}
	}

	var document map[string]any
	if err := common.ParseJSON(r, &document); err != nil {
		common.JSONError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	common.ResultMessage(w, "Document updated successfully", s.service.UpdateDocument(name, id, document))
}

func (s *Server) handleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	id := r.PathValue("id")
	dbName := r.URL.Query().Get("database")

	if dbName != "" {
		if err := s.service.SwitchDatabase(dbName); err != nil {
			common.JSONError(w, http.StatusInternalServerError, "Failed to switch database: "+err.Error())
			return
		}
	}

	common.ResultMessage(w, "Document deleted successfully", s.service.DeleteDocument(name, id))
}

func (s *Server) handleBulkDeleteDocuments(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	dbName := r.URL.Query().Get("database")

	if dbName != "" {
		if err := s.service.SwitchDatabase(dbName); err != nil {
			common.JSONError(w, http.StatusInternalServerError, "Failed to switch database: "+err.Error())
			return
		}
	}

	var req struct {
		IDs []string `json:"ids"`
	}
	if err := common.ParseJSON(r, &req); err != nil {
		common.JSONError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	common.ResultMessage(w, "Documents deleted successfully", s.service.BulkDeleteDocuments(name, req.IDs))
}

// Aggregation Handler
func (s *Server) handleAggregate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	dbName := r.URL.Query().Get("database")

	if dbName != "" {
		if err := s.service.SwitchDatabase(dbName); err != nil {
			common.JSONError(w, http.StatusInternalServerError, "Failed to switch database: "+err.Error())
			return
		}
	}

	var rawPipeline []any
	if err := common.ParseJSON(r, &rawPipeline); err != nil {
		common.JSONError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	pipeline := make([]bson.M, len(rawPipeline))
	for i, stage := range rawPipeline {
		if stageMap, ok := stage.(map[string]any); ok {
			pipeline[i] = bson.M(stageMap)
		}
	}

	result, err := s.service.Aggregate(name, pipeline)
	common.Result(w, result, err)
}

// Index Handlers
func (s *Server) handleGetIndexes(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	dbName := r.URL.Query().Get("database")

	if dbName != "" {
		if err := s.service.SwitchDatabase(dbName); err != nil {
			common.JSONError(w, http.StatusInternalServerError, "Failed to switch database: "+err.Error())
			return
		}
	}

	indexes, err := s.service.GetIndexes(name)
	common.Result(w, indexes, err)
}

func (s *Server) handleCreateIndex(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	dbName := r.URL.Query().Get("database")

	if dbName != "" {
		if err := s.service.SwitchDatabase(dbName); err != nil {
			common.JSONError(w, http.StatusInternalServerError, "Failed to switch database: "+err.Error())
			return
		}
	}

	var req struct {
		Keys   map[string]any `json:"keys"`
		Unique bool           `json:"unique"`
	}
	if err := common.ParseJSON(r, &req); err != nil {
		common.JSONError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	common.ResultMessage(w, "Index created successfully", s.service.CreateIndex(name, req.Keys, req.Unique))
}

func (s *Server) handleDropIndex(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	indexName := r.PathValue("indexName")
	dbName := r.URL.Query().Get("database")

	if dbName != "" {
		if err := s.service.SwitchDatabase(dbName); err != nil {
			common.JSONError(w, http.StatusInternalServerError, "Failed to switch database: "+err.Error())
			return
		}
	}

	common.ResultMessage(w, "Index dropped successfully", s.service.DropIndex(name, indexName))
}

// Schema Handler
func (s *Server) handleGetSchema(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	dbName := r.URL.Query().Get("database")
	if dbName == "" {
		common.JSONError(w, http.StatusBadRequest, "database parameter is required")
		return
	}

	schema, err := s.service.GetCollectionSchema(dbName, name)
	common.Result(w, schema, err)
}

// Query Handler
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	var req struct {
		Filter string `json:"filter"`
		Limit  int    `json:"limit"`
	}
	if err := common.ParseJSON(r, &req); err != nil {
		common.JSONError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	var filter bson.M
	if err := json.Unmarshal([]byte(req.Filter), &filter); err != nil {
		common.JSONError(w, http.StatusBadRequest, "Invalid filter format")
		return
	}

	if req.Limit == 0 {
		req.Limit = 100
	}
	if req.Limit > 1000 {
		req.Limit = 1000
	}

	result, err := s.service.Query(name, filter, req.Limit)
	common.Result(w, result, err)
}

// Stats Handlers
func (s *Server) handleGetStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.service.GetStats()
	common.Result(w, stats, err)
}

func (s *Server) handleGetCollectionStats(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	stats, err := s.service.GetCollectionStats(name)
	common.Result(w, stats, err)
}
