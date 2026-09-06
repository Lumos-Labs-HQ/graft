//go:build plugin_core || dev

package cmd

import (
	"fmt"

	"github.com/Lumos-Labs-HQ/flash/internal/config"
	"github.com/Lumos-Labs-HQ/flash/internal/gogen"
	"github.com/Lumos-Labs-HQ/flash/internal/javagen"
	"github.com/Lumos-Labs-HQ/flash/internal/jsgen"
	"github.com/Lumos-Labs-HQ/flash/internal/kotlingen"
	"github.com/Lumos-Labs-HQ/flash/internal/pygen"
	"github.com/Lumos-Labs-HQ/flash/internal/rustgen"
	"github.com/spf13/cobra"
)

var genCmd = &cobra.Command{
	Use:   "gen",
	Short: "Generate code from SQL",
	Long: `
Generate type-safe code from SQL queries.
Automatically detects project type and generates appropriate code:
- Go projects: Generate Go code with custom generator
- Node.js projects: Generate JavaScript code with type annotations
- Python projects: Generate Python code with type hints
- Kotlin projects: Generate Kotlin code with JDBC/Exposed/R2DBC support
- Java projects: Generate Java code with JDBC/jOOQ/Hibernate support
- Rust projects: Generate Rust code with sqlx support

Configuration is read from flash.toml`,

	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		force, _ := cmd.Flags().GetBool("force")
		dbName, _ := cmd.Flags().GetString("db")

		// Multi-database: if --db specified or config has [[databases]], resolve
		if cfg.IsMultiDB() {
			if dbName == "" {
				// Check for a default database
				dbName = cfg.GetDefaultDB()
			}
			if dbName != "" {
				// Generate for single named database
				cfg, err = cfg.ResolveForDB(dbName)
				if err != nil {
					return err
				}
				cfg.ForceRegen = force
				return runGenForConfig(cfg)
			}
			// No default, no --db: generate for ALL databases
			for _, db := range cfg.Databases {
				resolved, err := cfg.ResolveForDB(db.Name)
				if err != nil {
					return err
				}
				resolved.ForceRegen = force
				fmt.Printf("📦 Database: %s (%s)\n", db.Name, db.Provider)
				if err := runGenForConfig(resolved); err != nil {
					return fmt.Errorf("[%s] %w", db.Name, err)
				}
			}
			return nil
		}

		cfg.ForceRegen = force
		return runGenForConfig(cfg)
	},
}

func runGenForConfig(cfg *config.Config) error {
	generated := false

	if cfg.Gen.JS.Enabled {
		fmt.Println("🔨 Generating JavaScript code...")
		generator := jsgen.New(cfg)
		if err := generator.Generate(); err != nil {
			return fmt.Errorf("failed to generate JavaScript code: %w", err)
		}
		fmt.Println("🎉 JavaScript code generated successfully!")
		fmt.Printf("   Output: %s\n", cfg.Gen.JS.Out)
		generated = true
	}

	if cfg.Gen.Python.Enabled {
		fmt.Println("🔨 Generating Python code...")
		generator := pygen.New(cfg)
		if err := generator.Generate(); err != nil {
			return fmt.Errorf("failed to generate Python code: %w", err)
		}
		fmt.Println("🎉 Python code generated successfully!")
		fmt.Printf("   Output: %s\n", cfg.Gen.Python.Out)
		generated = true
	}

	if cfg.Gen.Kotlin.Enabled {
		fmt.Println("🔨 Generating Kotlin code...")
		generator := kotlingen.New(cfg)
		if err := generator.Generate(); err != nil {
			return fmt.Errorf("failed to generate Kotlin code: %w", err)
		}
		fmt.Println("🎉 Kotlin code generated successfully!")
		fmt.Printf("   Output: %s\n", cfg.Gen.Kotlin.Out)
		generated = true
	}

	if cfg.Gen.Java.Enabled {
		fmt.Println("🔨 Generating Java code...")
		generator := javagen.New(cfg)
		if err := generator.Generate(); err != nil {
			return fmt.Errorf("failed to generate Java code: %w", err)
		}
		fmt.Println("🎉 Java code generated successfully!")
		fmt.Printf("   Output: %s\n", cfg.Gen.Java.Out)
		generated = true
	}

	if cfg.Gen.Rust.Enabled {
		fmt.Println("🔨 Generating Rust code...")
		generator := rustgen.New(cfg)
		if err := generator.Generate(); err != nil {
			return fmt.Errorf("failed to generate Rust code: %w", err)
		}
		fmt.Println("🎉 Rust code generated successfully!")
		fmt.Printf("   Output: %s/\n", cfg.Gen.Rust.Out)
		generated = true
	}

	if cfg.Gen.Go.Enabled || !generated {
		fmt.Println("🔨 Generating Go code...")
		generator := gogen.New(cfg)
		if err := generator.Generate(); err != nil {
			return fmt.Errorf("failed to generate Go code: %w", err)
		}
		fmt.Println("🎉 Go code generated successfully!")
		fmt.Printf("   Output: %s/\n", cfg.Gen.Go.Out)
	}

	return nil
}

func init() {
	// Command is registered by plugin executors, not the base CLI
}
