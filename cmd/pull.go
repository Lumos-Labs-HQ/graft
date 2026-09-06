//go:build plugin_core || dev

package cmd

import (
	"fmt"

	"github.com/Lumos-Labs-HQ/flash/internal/pull"
	"github.com/spf13/cobra"
)

var pullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Pull database schema to update local schema files",
	Long: `
Pull the current database schema and update the local schema files.
This command introspects the current database and intelligently updates your schema.

Smart behavior:
- If no schema files exist: creates single schema.sql with everything
- If schema files exist: compares and updates only changed parts in-place  
- New tables: creates new .sql file for that table
- Changed columns: updates only those lines in existing files

The command will:
1. Connect to the database
2. Introspect all tables, columns, indexes, and constraints
3. Compare with existing schema files
4. Update only what changed, create new files for new tables
5. Optionally create a backup before making changes`,

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

		backup, _ := cmd.Flags().GetBool("backup")
		outputPath, _ := cmd.Flags().GetString("output")

		pullService, err := pull.NewService(cfg)
		if err != nil {
			return fmt.Errorf("failed to create pull service: %w", err)
		}
		defer pullService.Close()

		opts := pull.Options{
			Backup:     backup,
			OutputPath: outputPath,
		}

		return pullService.PullSchema(ctx, opts)
	},
}

func init() {
	// Command is registered by plugin executors, not the base CLI
	pullCmd.Flags().BoolP("backup", "b", false, "Create backup of existing schema files before overwriting")
	pullCmd.Flags().StringP("output", "o", "", "Custom output path for schema directory")
}
