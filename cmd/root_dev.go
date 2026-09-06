//go:build dev

package cmd

import (
	"fmt"
	"os"

	"github.com/fatih/color"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/Lumos-Labs-HQ/flash/internal/config"
)

var (
	cfgFile string
	envName string
	Version = "2.8.0-beta-dev"
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
		"║     ▓  Go • TS • JS • Python • Kotlin • Java  • ORM  ▓       ║",
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
	color.New(color.FgMagenta, color.Bold).Println("                        [DEVELOPMENT BUILD]")
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

	// NO PLUGIN CHECK IN DEV MODE
	Run: func(cmd *cobra.Command, args []string) {
		showVersion, _ := cmd.Flags().GetBool("version")
		if showVersion {
			fmt.Printf("FlashORM CLI version %s\n", Version)
			os.Exit(0)
		}

		if len(args) == 0 {
			showBanner()
			fmt.Println()
			cmd.Help()
		}
	},
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ./flash.toml)")
	rootCmd.PersistentFlags().StringVar(&envName, "env", "", "environment name to load (loads .env.{name}, e.g. --env prod loads .env.prod)")
	rootCmd.PersistentFlags().String("db", "", "database name (for multi-database configs)")
	rootCmd.PersistentFlags().BoolP("force", "f", false, "Skip confirmations")
	rootCmd.Flags().BoolP("version", "v", false, "Show CLI version")
}

func initConfig() {
	if envName != "" {
		godotenv.Load(".env." + envName)
	}
	godotenv.Load(".env")
	godotenv.Load(".env.local")

	config.ConfigFile = cfgFile

	// If flash.toml specifies env_path, load that file too (overrides defaults above)
	if cfg, err := config.Load(); err == nil && cfg.EnvPath != "" {
		godotenv.Overload(cfg.EnvPath)
	}
}
