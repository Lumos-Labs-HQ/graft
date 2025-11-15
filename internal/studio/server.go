package studio

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"

	"github.com/Lumos-Labs-HQ/flash/internal/config"
	"github.com/Lumos-Labs-HQ/flash/internal/database"
	"github.com/Lumos-Labs-HQ/flash/internal/branch"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/filesystem"
	"github.com/gofiber/template/html/v2"
)

type Server struct {
	app     *fiber.App
	service *Service
	port    int
}

func NewServer(cfg *config.Config, port int) *Server {
	adapter := database.NewAdapter(cfg.Database.Provider)

	dbURL, err := cfg.GetDatabaseURL()
	if err != nil {
		panic(fmt.Sprintf("Failed to get database URL: %v", err))
	}

	// Use embedded templates
	if err := adapter.Connect(context.Background(), dbURL); err != nil {
		panic(fmt.Sprintf("Failed to connect to database: %v", err))
	}

	ctx := context.Background()
	branchMgr := branch.NewMetadataManager(cfg.MigrationsPath)
	if store, err := branchMgr.Load(); err == nil {
		if currentBranch := store.GetBranch(store.Current); currentBranch != nil {
			if cfg.Database.Provider == "postgresql" || cfg.Database.Provider == "postgres" {
				query := fmt.Sprintf("SET search_path TO %s, public", currentBranch.Schema)
				adapter.ExecuteQuery(ctx, query)
				fmt.Printf("🔧 Studio using schema: %s (branch: %s)\n", currentBranch.Schema, currentBranch.Name)
			}
		}
	}

	// Use embedded templates
	engine := html.NewFileSystem(http.FS(TemplatesFS), ".html")

	app := fiber.New(fiber.Config{
		Views: engine,
	})

	server := &Server{
		app:     app,
		service: NewService(adapter),
		port:    port,
	}

	server.setupRoutes()
	return server
}

func (s *Server) setupRoutes() {
	staticFS, _ := fs.Sub(StaticFS, "static")
	s.app.Use("/static", filesystem.New(filesystem.Config{
		Root: http.FS(staticFS),
	}))

	// UI
	s.app.Get("/", s.handleIndex)
	s.app.Get("/schema", s.handleSchema)
	s.app.Get("/sql", s.handleSQL)

	// API
	api := s.app.Group("/api")
	api.Get("/tables", s.handleGetTables)
	api.Get("/tables/:name", s.handleGetTableData)
	api.Get("/schema", s.handleGetSchema)
	api.Post("/tables/:name/save", s.handleSaveChanges)
	api.Post("/tables/:name/add", s.handleAddRow)
	api.Post("/tables/:name/delete", s.handleDeleteRows)
	api.Delete("/tables/:name/rows/:id", s.handleDeleteRow)
	api.Post("/sql", s.handleExecuteSQL)

	// Schema Editor API
	api.Post("/schema/preview", s.handlePreviewSchemaChange)
	api.Post("/schema/apply", s.handleApplySchemaChange)
	api.Get("/config/check", s.handleCheckConfig)
	api.Put("/tables/:name/rows/:id", s.handleUpdateRow)
	api.Post("/tables/:name/rows", s.handleInsertRow)

	// Branch API
	api.Get("/branches", s.handleGetBranches)
	api.Post("/branches/switch", s.handleSwitchBranch)
}

func (s *Server) Start(openBrowser bool) error {
	url := fmt.Sprintf("http://localhost:%d", s.port)

	fmt.Printf("🚀 FlashORM Studio starting on %s\n", url)

	if openBrowser {
		go s.openBrowser(url)
	}

	return s.app.Listen(fmt.Sprintf(":%d", s.port))
}

func (s *Server) openBrowser(url string) {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start", url}
	case "darwin":
		cmd = "open"
		args = []string{url}
	default:
		cmd = "xdg-open"
		args = []string{url}
	}

	exec.Command(cmd, args...).Start()
}

// Handlers
func (s *Server) handleIndex(c *fiber.Ctx) error {
	return c.Render("templates/index", fiber.Map{
		"Title": "FlashORM Studio",
	})
}

func (s *Server) handleSchema(c *fiber.Ctx) error {
	return c.Render("templates/schema", fiber.Map{
		"Title": "FlashORM Studio",
	})
}

func (s *Server) handleSQL(c *fiber.Ctx) error {
	return c.Render("templates/sql", fiber.Map{
		"Title": "SQL Editor - FlashORM Studio",
	})
}

func (s *Server) handleGetSchema(c *fiber.Ctx) error {
	schema, err := s.service.GetSchemaVisualization()
	if err != nil {
		return c.Status(500).JSON(Response{
			Success: false,
			Message: err.Error(),
		})
	}

	return c.JSON(Response{
		Success: true,
		Data:    schema,
	})
}

func (s *Server) handleGetTables(c *fiber.Ctx) error {
	tables, err := s.service.GetTables()
	if err != nil {
		return c.Status(500).JSON(Response{
			Success: false,
			Message: err.Error(),
		})
	}

	return c.JSON(Response{
		Success: true,
		Data:    tables,
	})
}

func (s *Server) handleGetTableData(c *fiber.Ctx) error {
	tableName := c.Params("name")
	page, _ := strconv.Atoi(c.Query("page", "1"))
	limit, _ := strconv.Atoi(c.Query("limit", "50"))

	data, err := s.service.GetTableData(tableName, page, limit)
	if err != nil {
		return c.Status(500).JSON(Response{
			Success: false,
			Message: err.Error(),
		})
	}

	return c.JSON(Response{
		Success: true,
		Data:    data,
	})
}

func (s *Server) handleSaveChanges(c *fiber.Ctx) error {
	tableName := c.Params("name")

	var req SaveRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(Response{
			Success: false,
			Message: "Invalid request",
		})
	}

	if err := s.service.SaveChanges(tableName, req.Changes); err != nil {
		return c.Status(500).JSON(Response{
			Success: false,
			Message: err.Error(),
		})
	}

	return c.JSON(Response{
		Success: true,
		Message: "Changes saved successfully",
	})
}

func (s *Server) handleAddRow(c *fiber.Ctx) error {
	tableName := c.Params("name")

	var req AddRowRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(Response{
			Success: false,
			Message: "Invalid request",
		})
	}

	if err := s.service.AddRow(tableName, req.Data); err != nil {
		return c.Status(500).JSON(Response{
			Success: false,
			Message: err.Error(),
		})
	}

	return c.JSON(Response{
		Success: true,
		Message: "Row added successfully",
	})
}

func (s *Server) handleDeleteRow(c *fiber.Ctx) error {
	tableName := c.Params("name")
	rowID := c.Params("id")

	if err := s.service.DeleteRow(tableName, rowID); err != nil {
		return c.Status(500).JSON(Response{
			Success: false,
			Message: err.Error(),
		})
	}

	return c.JSON(Response{
		Success: true,
		Message: "Row deleted successfully",
	})
}

func (s *Server) handleDeleteRows(c *fiber.Ctx) error {
	tableName := c.Params("name")

	var req struct {
		RowIDs []string `json:"row_ids"`
	}

	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(Response{
			Success: false,
			Message: "Invalid request",
		})
	}

	if err := s.service.DeleteRows(tableName, req.RowIDs); err != nil {
		return c.Status(500).JSON(Response{
			Success: false,
			Message: err.Error(),
		})
	}

	return c.JSON(Response{
		Success: true,
		Message: fmt.Sprintf("Deleted %d row(s) successfully", len(req.RowIDs)),
	})
}

func (s *Server) handleExecuteSQL(c *fiber.Ctx) error {
	var req struct {
		Query string `json:"query"`
	}

	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(Response{
			Success: false,
			Message: "Invalid request",
		})
	}

	data, err := s.service.ExecuteSQL(req.Query)
	if err != nil {
		return c.Status(500).JSON(Response{
			Success: false,
			Message: err.Error(),
		})
	}

	return c.JSON(Response{
		Success: true,
		Data:    data,
	})
}
