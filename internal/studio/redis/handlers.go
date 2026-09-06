package redis

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/studio/common"
)

func (s *Server) handleGetInfo(w http.ResponseWriter, r *http.Request) {
	info, err := s.service.GetInfo()
	common.Result(w, info, err)
}

func (s *Server) handleGetDBSize(w http.ResponseWriter, r *http.Request) {
	size, err := s.service.GetDBSize()
	if err != nil {
		common.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	common.JSONMap(w, common.Map{"size": size})
}

func (s *Server) handleGetKeys(w http.ResponseWriter, r *http.Request) {
	pattern := common.Query(r, "pattern", "*")
	cursor, _ := strconv.ParseUint(common.Query(r, "cursor", "0"), 10, 64)
	count, _ := strconv.ParseInt(common.Query(r, "count", "100"), 10, 64)

	result, err := s.service.GetKeys(pattern, cursor, count)
	common.Result(w, result, err)
}

func (s *Server) handleGetKey(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		common.JSONError(w, http.StatusBadRequest, "key is required")
		return
	}

	keyInfo, err := s.service.GetKey(key)
	if err != nil {
		common.JSONError(w, http.StatusNotFound, err.Error())
		return
	}
	common.JSON(w, keyInfo)
}

func (s *Server) handleSetKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key   string `json:"key"`
		Value any    `json:"value"`
		Type  string `json:"type"`
		TTL   int64  `json:"ttl"`
	}

	if err := common.ParseJSON(r, &body); err != nil {
		common.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Key == "" {
		common.JSONError(w, http.StatusBadRequest, "key is required")
		return
	}

	if body.Type == "" {
		body.Type = "string"
	}

	common.ResultMessage(w, "key created successfully", s.service.SetKey(body.Key, body.Value, body.Type, body.TTL))
}

func (s *Server) handleUpdateKey(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		common.JSONError(w, http.StatusBadRequest, "key is required")
		return
	}

	var body struct {
		Value any    `json:"value"`
		Type  string `json:"type"`
		TTL   int64  `json:"ttl"`
	}

	if err := common.ParseJSON(r, &body); err != nil {
		common.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Type == "" {
		existingKey, err := s.service.GetKey(key)
		if err != nil {
			common.JSONError(w, http.StatusNotFound, err.Error())
			return
		}
		body.Type = existingKey.Type
	}

	common.ResultMessage(w, "key updated successfully", s.service.SetKey(key, body.Value, body.Type, body.TTL))
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		common.JSONError(w, http.StatusBadRequest, "key is required")
		return
	}

	if err := s.service.DeleteKey(key); err != nil {
		common.JSONError(w, http.StatusNotFound, err.Error())
		return
	}

	common.JSONMessage(w, "key deleted successfully")
}

func (s *Server) handleBulkDeleteKeys(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Keys []string `json:"keys"`
	}

	if err := common.ParseJSON(r, &body); err != nil {
		common.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(body.Keys) == 0 {
		common.JSONError(w, http.StatusBadRequest, "keys array is required")
		return
	}

	deleted, err := s.service.BulkDeleteKeys(body.Keys)
	if err != nil {
		common.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	common.JSONMap(w, common.Map{
		"success": true,
		"message": "keys deleted successfully",
		"deleted": deleted,
	})
}

func (s *Server) handleCLI(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Command string `json:"command"`
	}

	if err := common.ParseJSON(r, &body); err != nil {
		common.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Command == "" {
		common.JSONError(w, http.StatusBadRequest, "command is required")
		return
	}

	result := s.service.ExecuteCLI(body.Command)
	common.JSON(w, result)
}

func (s *Server) handleGetDatabases(w http.ResponseWriter, r *http.Request) {
	databases, err := s.service.GetDatabases()
	common.Result(w, databases, err)
}

func (s *Server) handleSelectDatabase(w http.ResponseWriter, r *http.Request) {
	dbStr := r.PathValue("db")
	db, err := strconv.Atoi(dbStr)
	if err != nil {
		common.JSONError(w, http.StatusBadRequest, "invalid database index")
		return
	}

	common.ResultMessage(w, "database selected successfully", s.service.SelectDatabase(db))
}

func (s *Server) handleFlushDB(w http.ResponseWriter, r *http.Request) {
	common.ResultMessage(w, "database flushed successfully", s.service.FlushDB())
}

func (s *Server) handleExportKeys(w http.ResponseWriter, r *http.Request) {
	pattern := common.Query(r, "pattern", "*")

	keys, err := s.service.ExportKeys(pattern)
	if err != nil {
		common.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	common.JSON(w, common.Map{
		"keys":  keys,
		"count": len(keys),
	})
}

func (s *Server) handleImportKeys(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Keys      []ExportedKey `json:"keys"`
		Overwrite bool          `json:"overwrite"`
	}

	if err := common.ParseJSON(r, &body); err != nil {
		common.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	imported, skipped, err := s.service.ImportKeys(body.Keys, body.Overwrite)
	if err != nil {
		common.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	common.JSONMap(w, common.Map{
		"success":  true,
		"imported": imported,
		"skipped":  skipped,
	})
}

func (s *Server) handleGetMemoryStats(w http.ResponseWriter, r *http.Request) {
	pattern := common.Query(r, "pattern", "*")
	limit, _ := strconv.Atoi(common.Query(r, "limit", "100"))

	memoryInfos, typeStats, err := s.service.GetMemoryStats(pattern, limit)
	if err != nil {
		common.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	common.JSON(w, common.Map{
		"keys":       memoryInfos,
		"type_stats": typeStats,
	})
}

func (s *Server) handleGetMemoryOverview(w http.ResponseWriter, r *http.Request) {
	overview, err := s.service.GetMemoryOverview()
	common.Result(w, overview, err)
}

func (s *Server) handleGetKeyMemory(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		common.JSONError(w, http.StatusBadRequest, "key is required")
		return
	}

	info, err := s.service.GetKeyMemory(key)
	common.Result(w, info, err)
}

func (s *Server) handleGetSlowLog(w http.ResponseWriter, r *http.Request) {
	count, _ := strconv.Atoi(common.Query(r, "count", "50"))

	entries, err := s.service.GetSlowLog(count)
	common.Result(w, entries, err)
}

func (s *Server) handleResetSlowLog(w http.ResponseWriter, r *http.Request) {
	common.ResultMessage(w, "slow log reset successfully", s.service.ResetSlowLog())
}

func (s *Server) handleGetSlowLogLen(w http.ResponseWriter, r *http.Request) {
	length, err := s.service.GetSlowLogLen()
	if err != nil {
		common.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	common.JSONMap(w, common.Map{"length": length})
}

func (s *Server) handleExecuteScript(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Script string   `json:"script"`
		Keys   []string `json:"keys"`
		Args   []any    `json:"args"`
	}

	if err := common.ParseJSON(r, &body); err != nil {
		common.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Script == "" {
		common.JSONError(w, http.StatusBadRequest, "script is required")
		return
	}

	if body.Keys == nil {
		body.Keys = []string{}
	}
	if body.Args == nil {
		body.Args = []any{}
	}

	result := s.service.ExecuteScript(body.Script, body.Keys, body.Args)
	common.JSON(w, result)
}

func (s *Server) handleLoadScript(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Script string `json:"script"`
	}

	if err := common.ParseJSON(r, &body); err != nil {
		common.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	sha, err := s.service.LoadScript(body.Script)
	if err != nil {
		common.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	common.JSONMap(w, common.Map{"sha": sha})
}

func (s *Server) handleExecuteScriptBySHA(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SHA  string   `json:"sha"`
		Keys []string `json:"keys"`
		Args []any    `json:"args"`
	}

	if err := common.ParseJSON(r, &body); err != nil {
		common.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.SHA == "" {
		common.JSONError(w, http.StatusBadRequest, "sha is required")
		return
	}

	if body.Keys == nil {
		body.Keys = []string{}
	}
	if body.Args == nil {
		body.Args = []any{}
	}

	result := s.service.ExecuteScriptBySHA(body.SHA, body.Keys, body.Args)
	common.JSON(w, result)
}

func (s *Server) handleFlushScripts(w http.ResponseWriter, r *http.Request) {
	common.ResultMessage(w, "scripts flushed successfully", s.service.FlushScripts())
}

func (s *Server) handleBulkSetTTL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Pattern string `json:"pattern"`
		TTL     int64  `json:"ttl"`
	}

	if err := common.ParseJSON(r, &body); err != nil {
		common.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Pattern == "" {
		common.JSONError(w, http.StatusBadRequest, "pattern is required")
		return
	}

	updated, err := s.service.BulkSetTTL(body.Pattern, body.TTL)
	if err != nil {
		common.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	common.JSONMap(w, common.Map{
		"success": true,
		"updated": updated,
	})
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	pattern := common.Query(r, "pattern", "*")

	config, err := s.service.GetConfig(pattern)
	common.Result(w, config, err)
}

func (s *Server) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}

	if err := common.ParseJSON(r, &body); err != nil {
		common.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Key == "" {
		common.JSONError(w, http.StatusBadRequest, "key is required")
		return
	}

	common.ResultMessage(w, "configuration updated successfully", s.service.SetConfig(body.Key, body.Value))
}

func (s *Server) handleRewriteConfig(w http.ResponseWriter, r *http.Request) {
	common.ResultMessage(w, "configuration rewritten successfully", s.service.RewriteConfig())
}

func (s *Server) handleResetConfigStats(w http.ResponseWriter, r *http.Request) {
	common.ResultMessage(w, "statistics reset successfully", s.service.ResetConfigStats())
}

func (s *Server) handleGetReplicationInfo(w http.ResponseWriter, r *http.Request) {
	info, err := s.service.GetReplicationInfo()
	common.Result(w, info, err)
}

func (s *Server) handleGetClusterInfo(w http.ResponseWriter, r *http.Request) {
	info, err := s.service.GetClusterInfo()
	common.Result(w, info, err)
}

func (s *Server) handleGetACLUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.service.GetACLUsers()
	common.Result(w, users, err)
}

func (s *Server) handleGetACLUser(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	if username == "" {
		common.JSONError(w, http.StatusBadRequest, "username is required")
		return
	}

	user, err := s.service.GetACLUser(username)
	common.Result(w, user, err)
}

func (s *Server) handleCreateACLUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string   `json:"username"`
		Rules    []string `json:"rules"`
	}

	if err := common.ParseJSON(r, &body); err != nil {
		common.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Username == "" {
		common.JSONError(w, http.StatusBadRequest, "username is required")
		return
	}

	common.ResultMessage(w, "user created successfully", s.service.CreateACLUser(body.Username, body.Rules))
}

func (s *Server) handleDeleteACLUser(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	if username == "" {
		common.JSONError(w, http.StatusBadRequest, "username is required")
		return
	}

	common.ResultMessage(w, "user deleted successfully", s.service.DeleteACLUser(username))
}

func (s *Server) handleGetACLLog(w http.ResponseWriter, r *http.Request) {
	count, _ := strconv.Atoi(common.Query(r, "count", "10"))

	logs, err := s.service.GetACLLog(count)
	common.Result(w, logs, err)
}

func (s *Server) handleResetACLLog(w http.ResponseWriter, r *http.Request) {
	common.ResultMessage(w, "ACL log reset successfully", s.service.ResetACLLog())
}

func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Channel string `json:"channel"`
		Message string `json:"message"`
	}

	if err := common.ParseJSON(r, &body); err != nil {
		common.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Channel == "" {
		common.JSONError(w, http.StatusBadRequest, "channel is required")
		return
	}

	receivers, err := s.service.Publish(body.Channel, body.Message)
	if err != nil {
		common.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	common.JSONMap(w, common.Map{
		"success":   true,
		"receivers": receivers,
	})
}

func (s *Server) handleGetChannels(w http.ResponseWriter, r *http.Request) {
	pattern := common.Query(r, "pattern", "*")

	channels, err := s.service.GetPubSubChannels(pattern)
	if err != nil {
		common.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Get subscriber counts
	var numSub map[string]int64
	if len(channels) > 0 {
		numSub, _ = s.service.GetPubSubNumSub(channels)
	}

	result := make([]common.Map, 0, len(channels))
	for _, ch := range channels {
		count := int64(0)
		if numSub != nil {
			count = numSub[ch]
		}
		result = append(result, common.Map{
			"channel":     ch,
			"subscribers": count,
		})
	}

	numPat, _ := s.service.GetPubSubNumPat()

	common.JSON(w, common.Map{
		"channels":            result,
		"pattern_subscribers": numPat,
	})
}

func (s *Server) handleGetExtendedInfo(w http.ResponseWriter, r *http.Request) {
	section := common.Query(r, "section", "all")

	var info string
	var err error

	if section == "all" {
		info, err = s.service.client.Info(s.service.ctx).Result()
	} else {
		info, err = s.service.client.Info(s.service.ctx, section).Result()
	}

	if err != nil {
		common.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Parse into sections
	result := make(map[string]map[string]string)
	currentSection := "default"

	lines := strings.SplitSeq(info, "\n")
	for line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			currentSection = strings.TrimPrefix(line, "# ")
			result[currentSection] = make(map[string]string)
		} else if strings.Contains(line, ":") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				if result[currentSection] == nil {
					result[currentSection] = make(map[string]string)
				}
				result[currentSection][parts[0]] = parts[1]
			}
		}
	}

	common.JSON(w, result)
}
