//go:build plugin_core || plugin_studio || dev

package cmd

import (
	"fmt"

	"github.com/Lumos-Labs-HQ/flash/internal/config"
	"github.com/spf13/cobra"
)

// loadConfigForDB loads config and resolves for the --db flag if specified.
func loadConfigForDB(cmd *cobra.Command) (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	dbName, _ := cmd.Flags().GetString("db")
	if dbName != "" {
		cfg, err = cfg.ResolveForDB(dbName)
		if err != nil {
			return nil, err
		}
	} else if cfg.IsMultiDB() {
		// Default to first database in multi-db mode
		cfg, err = cfg.ResolveForDB(cfg.Databases[0].Name)
		if err != nil {
			return nil, err
		}
	}

	return cfg, nil
}
