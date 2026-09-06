//go:build plugin_core || dev

package cmd

import (
	"fmt"

	"github.com/Lumos-Labs-HQ/flash/internal/migrator"

	"github.com/spf13/cobra"
)

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply pending migrations",
	Long: `
Apply all pending migrations to the database.

This command will:
1. Check for migration conflicts
2. Prompt for backup if conflicts are detected
3. Apply all pending migrations in orderC
4. Update migration tracking table

	Use --force to skip confirmation prompts.`,
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

		// Get current branch info
		branchName, branchSchema, err := migrator.GetCurrentBranchInfo(cfg)
		if err == nil && branchName != "main" {
			fmt.Printf("📍 Applying migrations to branch: %s (schema: %s)\n", branchName, branchSchema)
		}

		bam, err := migrator.NewBranchAwareMigrator(cfg)
		if err != nil {
			return fmt.Errorf("failed to create migrator: %w", err)
		}
		defer bam.Close()

		force, _ := cmd.Flags().GetBool("force")
		bam.SetForce(force)

		return bam.Apply(ctx, "", cfg.SchemaPath)
	},
}

func init() {
	// Command is registered by plugin executors, not the base CLI
	applyCmd.Flags().BoolP("force", "f", false, "Skip confirmation prompts")
}
