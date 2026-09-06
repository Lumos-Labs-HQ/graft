//go:build plugin_core || dev

package cmd

import (
	"fmt"

	"github.com/Lumos-Labs-HQ/flash/internal/migrator"

	"github.com/spf13/cobra"
)

var downCmd = &cobra.Command{
	Use:   "down [migration_id]",
	Short: "Roll back migrations",
	Long: `
Roll back database migrations.

By default, rolls back the last applied migration.
You can specify a migration ID to roll back to that point (exclusive).
Use --steps to roll back multiple migrations.

Before destructive operations, you will be prompted to create an export.

Examples:
  flash down                    # Roll back last migration
  flash down --steps 3          # Roll back last 3 migrations
  flash down 20231201           # Roll back to migration 20231201 (exclusive)
  flash down --force            # Skip confirmation prompts`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfigForDB(cmd)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("invalid config: %w", err)
		}

		ctx := cmd.Context()

		branchName, branchSchema, err := migrator.GetCurrentBranchInfo(cfg)
		if err == nil && branchName != "main" {
			fmt.Printf("📍 Rolling back migrations on branch: %s (schema: %s)\n", branchName, branchSchema)
		}

		bam, err := migrator.NewBranchAwareMigrator(cfg)
		if err != nil {
			return fmt.Errorf("failed to create migrator: %w", err)
		}
		defer bam.Close()

		force, _ := cmd.Flags().GetBool("force")
		steps, _ := cmd.Flags().GetInt("steps")
		bam.SetForce(force)

		var targetMigrationID string
		if len(args) > 0 {
			targetMigrationID = args[0]
		}

		return bam.Down(ctx, targetMigrationID, steps)
	},
}

func init() {
	downCmd.Flags().BoolP("force", "f", false, "Skip confirmation prompts")
	downCmd.Flags().IntP("steps", "s", 0, "Number of migrations to roll back")
}
