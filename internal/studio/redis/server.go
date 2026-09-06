package redis

import (
	"context"
	"fmt"
	"net/http"

	"github.com/redis/go-redis/v9"

	"github.com/Lumos-Labs-HQ/flash/internal/studio/common"
)

type Server struct {
	*common.BaseServer
	service *Service
}

func NewServer(connectionURL string, port int, host, authToken string) *Server {
	opts, err := redis.ParseURL(connectionURL)
	if err != nil {
		panic(fmt.Sprintf("Failed to parse Redis URL: %v", err))
	}

	client := redis.NewClient(opts)

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		panic(fmt.Sprintf("Failed to connect to Redis: %v", err))
	}

	mux := http.NewServeMux()

	server := &Server{
		BaseServer: &common.BaseServer{
			Mux:           mux,
			Tmpl:          common.ParseTemplates(TemplatesFS),
			Port:          port,
			Host:          host,
			AuthToken:     authToken,
			ConnectionURL: connectionURL,
			Name:          "Redis Studio",
		},
		service: NewService(client),
	}

	server.setupRoutes()
	return server
}

func (s *Server) setupRoutes() {
	common.SetupStaticFS(s.Mux, StaticFS)

	// UI Routes
	s.Mux.HandleFunc("GET /{$}", s.handleIndex)

	// API Routes - Server info
	s.Mux.HandleFunc("GET /api/info", s.handleGetInfo)
	s.Mux.HandleFunc("GET /api/info/extended", s.handleGetExtendedInfo)
	s.Mux.HandleFunc("GET /api/dbsize", s.handleGetDBSize)

	// Key operations
	s.Mux.HandleFunc("GET /api/keys", s.handleGetKeys)
	s.Mux.HandleFunc("POST /api/keys", s.handleSetKey)
	s.Mux.HandleFunc("POST /api/keys/bulk-delete", s.handleBulkDeleteKeys)
	s.Mux.HandleFunc("GET /api/key", s.handleGetKey)
	s.Mux.HandleFunc("PUT /api/key", s.handleUpdateKey)
	s.Mux.HandleFunc("DELETE /api/key", s.handleDeleteKey)
	s.Mux.HandleFunc("POST /api/flush", s.handleFlushDB)

	// CLI
	s.Mux.HandleFunc("POST /api/cli", s.handleCLI)

	// Database selection
	s.Mux.HandleFunc("GET /api/databases", s.handleGetDatabases)
	s.Mux.HandleFunc("POST /api/databases/{db}", s.handleSelectDatabase)

	// Export/Import
	s.Mux.HandleFunc("GET /api/export", s.handleExportKeys)
	s.Mux.HandleFunc("POST /api/import", s.handleImportKeys)

	// Memory Analysis
	s.Mux.HandleFunc("GET /api/memory/stats", s.handleGetMemoryStats)
	s.Mux.HandleFunc("GET /api/memory/overview", s.handleGetMemoryOverview)
	s.Mux.HandleFunc("GET /api/memory/key", s.handleGetKeyMemory)

	// Slow Log
	s.Mux.HandleFunc("GET /api/slowlog", s.handleGetSlowLog)
	s.Mux.HandleFunc("DELETE /api/slowlog", s.handleResetSlowLog)
	s.Mux.HandleFunc("GET /api/slowlog/len", s.handleGetSlowLogLen)

	// Lua Scripting
	s.Mux.HandleFunc("POST /api/script/eval", s.handleExecuteScript)
	s.Mux.HandleFunc("POST /api/script/load", s.handleLoadScript)
	s.Mux.HandleFunc("POST /api/script/evalsha", s.handleExecuteScriptBySHA)
	s.Mux.HandleFunc("DELETE /api/scripts", s.handleFlushScripts)

	// Bulk TTL
	s.Mux.HandleFunc("POST /api/bulk-ttl", s.handleBulkSetTTL)

	// Config Management
	s.Mux.HandleFunc("GET /api/config", s.handleGetConfig)
	s.Mux.HandleFunc("PUT /api/config", s.handleSetConfig)
	s.Mux.HandleFunc("POST /api/config/rewrite", s.handleRewriteConfig)
	s.Mux.HandleFunc("POST /api/config/resetstat", s.handleResetConfigStats)

	// Cluster/Replication
	s.Mux.HandleFunc("GET /api/replication", s.handleGetReplicationInfo)
	s.Mux.HandleFunc("GET /api/cluster", s.handleGetClusterInfo)

	// ACL Management
	s.Mux.HandleFunc("GET /api/acl/users", s.handleGetACLUsers)
	s.Mux.HandleFunc("GET /api/acl/users/{username}", s.handleGetACLUser)
	s.Mux.HandleFunc("POST /api/acl/users", s.handleCreateACLUser)
	s.Mux.HandleFunc("DELETE /api/acl/users/{username}", s.handleDeleteACLUser)
	s.Mux.HandleFunc("GET /api/acl/log", s.handleGetACLLog)
	s.Mux.HandleFunc("DELETE /api/acl/log", s.handleResetACLLog)

	// Pub/Sub
	s.Mux.HandleFunc("POST /api/pubsub/publish", s.handlePublish)
	s.Mux.HandleFunc("GET /api/pubsub/channels", s.handleGetChannels)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	databases := make([]int, 16)
	for i := range 16 {
		databases[i] = i
	}
	s.Render(w, "index.html", common.Map{
		"Title":     "FlashORM Redis Studio",
		"Host":      maskRedisURL(s.ConnectionURL),
		"Databases": databases,
	})
}

func maskRedisURL(url string) string {
	if len(url) < 20 {
		return "redis://***"
	}
	return url[:10] + "***" + url[len(url)-5:]
}
