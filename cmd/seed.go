//go:build plugin_core || plugin_seed || dev

package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/seeder"
	"github.com/spf13/cobra"
)

var seedCmd = &cobra.Command{
	Use:   "seed [tables...]",
	Short: "Generate and insert fake test data",
	Long: `
Generate realistic fake data and insert it into your database tables.
Foreign key relationships are handled automatically.

Examples:
  flash seed                          # Seed all tables with 10 records each
  flash seed --count 100              # Seed all tables with 100 records each
  flash seed users                    # Seed only 'users' table
  flash seed users posts --count 50   # Seed specific tables with 50 records
  flash seed --truncate               # Clear tables before seeding
  flash seed --exclude logs,sessions  # Skip specific tables
  flash seed --dry-run                # Preview without inserting
  flash seed users:100 posts:500      # Custom count per table`,

	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfigForDB(cmd)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("invalid config: %w", err)
		}

		count, _ := cmd.Flags().GetInt("count")
		truncate, _ := cmd.Flags().GetBool("truncate")
		force, _ := cmd.Flags().GetBool("force")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		excludeRaw, _ := cmd.Flags().GetString("exclude")

		tableCounts := make(map[string]int)
		var specificTables []string

		for _, arg := range args {
			// Use LastIndex to handle schema-qualified names like public.users:100
			if idx := strings.LastIndex(arg, ":"); idx > 0 {
				table := arg[:idx]
				if n, err := strconv.Atoi(arg[idx+1:]); err == nil {
					tableCounts[table] = n
					specificTables = append(specificTables, table)
					continue
				}
			}
			specificTables = append(specificTables, arg)
		}

		for _, table := range specificTables {
			if _, exists := tableCounts[table]; !exists {
				tableCounts[table] = count
			}
		}

		var exclude []string
		if excludeRaw != "" {
			for e := range strings.SplitSeq(excludeRaw, ",") {
				if e = strings.TrimSpace(e); e != "" {
					exclude = append(exclude, e)
				}
			}
		}

		seedConfig := seeder.SeedConfig{
			Count:          count,
			Tables:         tableCounts,
			Truncate:       truncate,
			Force:          force,
			DryRun:         dryRun,
			Exclude:        exclude,
			SpecificTables: specificTables,
		}

		ctx := cmd.Context()
		s, err := seeder.NewSeeder(cfg)
		if err != nil {
			return fmt.Errorf("failed to create seeder: %w", err)
		}
		defer s.Close()

		return s.Seed(ctx, seedConfig)
	},
}

func init() {
	seedCmd.Flags().IntP("count", "c", 10, "Records per table")
	seedCmd.Flags().BoolP("truncate", "t", false, "Truncate tables before seeding")
	seedCmd.Flags().BoolP("force", "f", false, "Skip confirmations")
	seedCmd.Flags().BoolP("dry-run", "d", false, "Preview data without inserting")
	seedCmd.Flags().StringP("exclude", "x", "", "Comma-separated tables to skip")
}
