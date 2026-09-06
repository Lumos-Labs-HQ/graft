//go:build plugin_core || dev

package cmd

import (
	"fmt"

	"github.com/Lumos-Labs-HQ/flash/internal/migrator"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show migration status",
	Long: `
Show the current status of all migrations including:
- Total number of migrations
- Number of applied migrations  
- Number of pending migrations
- Detailed list of each migration with status and timestamp

This command helps you understand which migrations have been applied
and which are still pending.`,

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

		m, err := migrator.NewMigrator(cfg)
		if err != nil {
			return fmt.Errorf("failed to create migrator: %w", err)
		}
		defer m.Close()

		return m.Status(ctx)
	},
}

func init() {
	// Command is registered by plugin executors, not the base CLI
}
