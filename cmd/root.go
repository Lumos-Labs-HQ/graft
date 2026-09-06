//go:build !dev
// +build !dev

package cmd

import (
	"fmt"
	"os"

	"github.com/fatih/color"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/Lumos-Labs-HQ/flash/internal/config"
	"github.com/Lumos-Labs-HQ/flash/internal/plugin"
)

var (
	cfgFile string
	envName string
	Version = "2.8.23"
)

func showBanner() {
	greenColor := color.New(color.FgGreen, color.Bold)

	banner := []string{
		"╔══════════════════════════════════════════════════════════════╗",
		"║   	  ███████╗██╗      █████╗ ███████╗██╗  ██╗             ║",
		"║   	  ██╔════╝██║     ██╔══██╗██╔════╝██║  ██║              ║",
		"║   	  █████╗  ██║     ███████║███████╗███████║             ║",
		"║   	  ██╔══╝  ██║     ██╔══██║╚════██║██╔══██║              ║",
		"║   	  ██║     ███████╗██║  ██║███████║██║  ██║             ║",
		"║   	  ╚═╝     ╚══════╝╚═╝  ╚═╝╚══════╝╚═╝  ╚═╝              ║",
		"║                                                             ║",
		"║         ⚡ Lightning-Fast Type-Safe ORM ⚡                   ║",
		"║                                                              ║",
		"║     ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓            ║",
		"║     ▓                                                ▓       ║",
		"║     ▓ Go • TS/JS • Python • Kotlin • Java • Rust     ▓       ║",
		"║     ▓                                                ▓       ║",
		"║     ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓              ║",
		"╚══════════════════════════════════════════════════════════════╝",
	}

	for _, line := range banner {
		greenColor.Println(line)
	}

	fmt.Print("                        ")
	color.New(color.FgCyan, color.Bold).Print("Version: ")
	color.New(color.FgYellow, color.Bold).Printf("%s\n", Version)
}

var rootCmd = &cobra.Command{
	Use:   "flash",
	Short: "A type-safe ORM with code generation for Go, TypeScript, and JavaScript",
	Long: `
FlashORM is a powerful ORM and database toolkit that generates type-safe code
from your SQL schemas and queries for multiple programming languages.

Supported Languages:
- Go (native type-safe structs and methods)
- TypeScript (with full type definitions)
- JavaScript (with JSDoc comments)
- Python (with async support)
- Kotlin (JDBC / Exposed / R2DBC)
- Java (JDBC / jOOQ / Hibernate)

Database Support:
- PostgreSQL (with advanced features)
- MySQL (full compatibility)
- SQLite (embedded databases)
- ScyllaDB / Cassandra
- ClickHouse`,

	PersistentPreRunE: checkPluginRequirement,

	Run: func(cmd *cobra.Command, args []string) {
		showVersion, _ := cmd.Flags().GetBool("version")
		if showVersion {
			fmt.Printf("FlashORM CLI version %s\n", Version)
			os.Exit(0)
		}

		if len(args) == 0 {
			showBanner()
			fmt.Println()
			_ = cmd.Help()
		}
	},
}

func Execute() error {
	// Check if the first argument is a plugin command
	if len(os.Args) > 1 {
		commandName := os.Args[1]

		// Skip if it's a built-in command
		builtInCommands := []string{"plugins", "add-plug", "rm-plug", "update", "help", "completion", "--help", "-h", "--version", "-v"}
		isBuiltIn := false
		for _, cmd := range builtInCommands {
			if commandName == cmd {
				isBuiltIn = true
				break
			}
		}

		if !isBuiltIn {
			// Check if this command requires a plugin
			requiredPlugin, requiresPlugin := plugin.GetRequiredPlugin(commandName)
			if requiresPlugin {
				manager, err := plugin.NewManager()
				if err != nil {
					return fmt.Errorf("failed to initialize plugin manager: %w", err)
				}

				// Auto-install core plugin on first use
				if requiredPlugin == "core" {
					if err := manager.EnsureCorePlugin(); err != nil {
						return err
					}
					return manager.ExecutePlugin("core", os.Args[1:])
				}

				// For studio, check if installed
				if manager.IsPluginInstalled(requiredPlugin) {
					return manager.ExecutePlugin(requiredPlugin, os.Args[1:])
				}

				color.Red("❌ Command '%s' requires the '%s' plugin", commandName, requiredPlugin)
				fmt.Println()
				color.Cyan("📦 Install it using:")
				color.Cyan("   flash add-plug %s", requiredPlugin)
				return fmt.Errorf("missing required plugin: %s", requiredPlugin)
			}
		}
	}

	return rootCmd.Execute()
}

// checkPluginRequirement checks if a command requires a plugin and handles it
func checkPluginRequirement(cmd *cobra.Command, args []string) error {
	commandName := cmd.Name()
	if commandName == "flash" || commandName == "plugins" || commandName == "add-plug" ||
		commandName == "rm-plug" || commandName == "update" || commandName == "help" || commandName == "version" {
		return nil
	}

	requiredPlugin, requiresPlugin := plugin.GetRequiredPlugin(commandName)
	if !requiresPlugin {
		return nil
	}

	manager, err := plugin.NewManager()
	if err != nil {
		return fmt.Errorf("failed to initialize plugin manager: %w", err)
	}

	// Auto-install core plugin on first use
	if requiredPlugin == "core" {
		if err := manager.EnsureCorePlugin(); err != nil {
			return err
		}
		return manager.ExecutePlugin("core", os.Args[1:])
	}

	if manager.IsPluginInstalled(requiredPlugin) {
		return manager.ExecutePlugin(requiredPlugin, os.Args[1:])
	}

	color.Red("❌ Command '%s' requires the '%s' plugin", commandName, requiredPlugin)
	fmt.Println()
	color.Cyan("📦 Install it using:")
	color.Cyan("   flash add-plug %s", requiredPlugin)
	return fmt.Errorf("missing required plugin: %s", requiredPlugin)
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ./flash.toml)")
	rootCmd.PersistentFlags().StringVar(&envName, "env", "", "environment name to load (loads .env.{name}, e.g. --env prod loads .env.prod)")
	rootCmd.PersistentFlags().BoolP("force", "f", false, "Skip confirmations")
	rootCmd.Flags().BoolP("version", "v", false, "Show CLI version")
}

func initConfig() {
	if envName != "" {
		_ = godotenv.Load(".env." + envName)
	}
	_ = godotenv.Load(".env")
	_ = godotenv.Load(".env.local")

	config.ConfigFile = cfgFile

	// If flash.toml specifies env_path, load that file too (overrides defaults above)
	if cfg, err := config.Load(); err == nil && cfg.EnvPath != "" {
		_ = godotenv.Overload(cfg.EnvPath)
	}
}
