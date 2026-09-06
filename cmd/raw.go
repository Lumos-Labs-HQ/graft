//go:build plugin_core || dev

package cmd

import (
	"github.com/Lumos-Labs-HQ/flash/internal/utils"
	"github.com/spf13/cobra"
)

var rawCmd = &cobra.Command{
	Use:   "raw <sql-query-or-file>",
	Short: "Execute raw SQL query or SQL file against the database",
	Long: `
Execute a raw SQL query or SQL file directly against the database using the configured database adapter.

You can either:
  1. Pass a SQL file path
  2. Pass a SQL query directly (use -q flag)
  3. Pass a SQL query inline (without flag if it starts with SELECT/INSERT/UPDATE/DELETE)
	
Examples:
  flash raw script.sql
  flash raw queries/update_users.sql
  flash raw -q "SELECT * FROM users"
  flash raw "SELECT * FROM users WHERE id = 1"
  flash raw --file script.sql`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return utils.RunRaw(cmd, args, rawQueryFlag, rawFileFlag)
	},
}

var (
	rawQueryFlag bool
	rawFileFlag  bool
)

func init() {
	// Command is registered by plugin executors, not the base CLI
	rawCmd.Flags().BoolVarP(&rawQueryFlag, "query", "q", false, "Treat argument as SQL query instead of file")
	rawCmd.Flags().BoolVar(&rawFileFlag, "file", false, "Treat argument as file path (default if file exists)")
}
