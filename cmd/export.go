//go:build plugin_core || dev

package cmd

import (
	"fmt"

	"github.com/Lumos-Labs-HQ/flash/internal/database"
	"github.com/Lumos-Labs-HQ/flash/internal/export"
	"github.com/spf13/cobra"
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export database tables",
	Long: `
Export all database tables (excluding migration table) to JSON format.

Examples:
  flash export`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfigForDB(cmd)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("invalid config: %w", err)
		}

		if err := cfg.EnsureDirectories(); err != nil {
			return fmt.Errorf("failed to create directories: %w", err)
		}

		ctx := cmd.Context()

		adapter := database.NewAdapter(cfg.Database.Provider)

		dbURL, err := cfg.GetDatabaseURL()
		if err != nil {
			return err
		}

		if err := adapter.Connect(ctx, dbURL); err != nil {
			return fmt.Errorf("failed to connect to database: %w", err)
		}
		defer adapter.Close()

		if err := adapter.Ping(ctx); err != nil {
			return fmt.Errorf("failed to connect to database: %w", err)
		}

		exportPath, err := export.PerformExport(ctx, adapter, cfg.ExportPath)
		if err != nil {
			return err
		}

		if exportPath != "" {
			fmt.Printf("✅ Export completed: %s\n", exportPath)
		} else {
			fmt.Println("No export created (database is empty)")
		}

		return nil
	},
}

func init() {
	// Command is registered by plugin executors, not the base CLI
}
