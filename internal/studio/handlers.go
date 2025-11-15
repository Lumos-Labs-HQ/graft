package studio

import (
	"os"

	"github.com/gofiber/fiber/v2"
)

// handlePreviewSchemaChange previews a schema change
func (s *Server) handlePreviewSchemaChange(c *fiber.Ctx) error {
	var change SchemaChange
	if err := c.BodyParser(&change); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
	}

	preview, err := s.service.PreviewSchemaChange(&change)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(preview)
}

// handleApplySchemaChange applies a schema change
func (s *Server) handleApplySchemaChange(c *fiber.Ctx) error {
	var change SchemaChange
	if err := c.BodyParser(&change); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
	}

	configPath := ""
	if _, err := os.Stat("./flash.config.json"); err == nil {
		configPath = "./flash.config.json"
	} 

	if err := s.service.ApplySchemaChange(&change, configPath); err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	message := "Schema change applied successfully"
	if configPath == "" {
		message += " (migration files not created - no config found)"
	}

	return c.JSON(fiber.Map{
		"success": true,
		"message": message,
	})
}

// handleUpdateRow updates a single row
func (s *Server) handleUpdateRow(c *fiber.Ctx) error {
	table := c.Params("name")
	id := c.Params("id")

	var data map[string]interface{}
	if err := c.BodyParser(&data); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
	}

	if err := s.service.UpdateRow(table, id, data); err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(fiber.Map{"success": true})
}

// handleInsertRow inserts a new row
func (s *Server) handleInsertRow(c *fiber.Ctx) error {
	table := c.Params("name")

	var data map[string]interface{}
	if err := c.BodyParser(&data); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
	}

	if err := s.service.InsertRow(table, data); err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(fiber.Map{"success": true})
}

// handleCheckConfig checks if config files exist
func (s *Server) handleCheckConfig(c *fiber.Ctx) error {
	exists := false
	if _, err := os.Stat("./flash.config.json"); err == nil {
		exists = true
	} 

	return c.JSON(fiber.Map{
		"exists": exists,
	})
}


func (s *Server) handleGetBranches(c *fiber.Ctx) error {
	branches, current, err := s.service.GetBranches()
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(fiber.Map{
		"branches": branches,
		"current":  current,
	})
}

// handleSwitchBranch switches to a different branch
func (s *Server) handleSwitchBranch(c *fiber.Ctx) error {
	var req struct {
		Branch string `json:"branch"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
	}

	if err := s.service.SwitchBranch(req.Branch); err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	
	return c.JSON(fiber.Map{
		"success": true,
		"message": "Branch switched. Please refresh the page to see changes.",
	})
}
