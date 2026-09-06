//go:build plugin_core || dev

package cmd

import (
	"fmt"

	"github.com/Lumos-Labs-HQ/flash/internal/config"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var dblistCmd = &cobra.Command{
	Use:   "dblist",
	Short: "List all configured databases",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if !cfg.IsMultiDB() {
			color.Cyan("Single database mode:")
			fmt.Printf("  provider:   %s\n", cfg.Database.Provider)
			fmt.Printf("  url_env:    %s\n", cfg.Database.URLEnv)
			fmt.Printf("  schema:     %s\n", cfg.GetSchemaDir())
			fmt.Printf("  queries:    %s\n", cfg.Queries)
			fmt.Printf("  migrations: %s\n", cfg.MigrationsPath)
			return nil
		}

		color.Cyan("Configured databases (%d):\n", len(cfg.Databases))
		for _, db := range cfg.Databases {
			if db.Default {
				color.Green("  • %s (default)", db.Name)
			} else {
				color.Green("  • %s", db.Name)
			}
			fmt.Printf("    provider:   %s\n", db.Provider)
			fmt.Printf("    url_env:    %s\n", db.URLEnv)
			fmt.Printf("    schema:     %s\n", db.SchemaDir)
			fmt.Printf("    queries:    %s\n", db.Queries)
			fmt.Printf("    migrations: %s\n", db.MigrationsPath)
			fmt.Println()
		}
		return nil
	},
}
