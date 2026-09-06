//go:build plugin_studio || dev

package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/Lumos-Labs-HQ/flash/internal/config"
	"github.com/Lumos-Labs-HQ/flash/internal/studio"
	"github.com/Lumos-Labs-HQ/flash/internal/studio/mongodb"
	"github.com/Lumos-Labs-HQ/flash/internal/studio/redis"
	"github.com/spf13/cobra"
)

var studioCmd = &cobra.Command{
	Use:   "studio [URL]",
	Short: "Open FlashORM Studio - Visual database editor",
	Long: `
Launch FlashORM Studio, a web-based interface for viewing and editing your database.
Similar to Prisma Studio, it provides an intuitive UI for managing your data.

The studio will start a local web server and open in your default browser.

Pass a database or Redis URL as a positional argument to auto-detect and launch
the appropriate studio. If no URL is given, the studio loads from flash.toml.

Examples:
  flash studio
  flash studio "postgres://user:pass@localhost:5432/mydb"
  flash studio "mongodb://localhost:27017/mydb"
  flash studio "redis://localhost:6379"
  flash studio "sqlite:///path/to/db.sqlite"
  flash studio --port 3000`,
	RunE: func(cmd *cobra.Command, args []string) error {
		port, _ := cmd.Flags().GetInt("port")
		browser, _ := cmd.Flags().GetBool("browser")
		host, _ := cmd.Flags().GetString("host")
		authToken, _ := cmd.Flags().GetString("auth-token")

		if host == "0.0.0.0" && authToken == "" {
			return fmt.Errorf("refusing to start on 0.0.0.0 without --auth-token (use 127.0.0.1 for local-only access)")
		}

		// If a positional argument is provided, auto-detect the studio type from the URL
		if len(args) > 0 && args[0] != "" {
			url := strings.TrimSpace(args[0])
			studioType := detectStudioType(url)

			switch studioType {
			case "redis":
				fmt.Printf("🔴 Starting Redis Studio: %s\n", maskDBURL(url))
				redisServer := redis.NewServer(url, port, host, authToken)
				return redisServer.Start(browser)
			case "mongodb":
				fmt.Printf("🍃 Starting MongoDB Studio: %s\n", maskDBURL(url))
				provider := "mongodb"
				cfg := &config.Config{
					Database: config.Database{
						Provider: provider,
						URLEnv:   "STUDIO_DB_URL",
					},
				}
				os.Setenv("STUDIO_DB_URL", url)
				mongoServer := mongodb.NewServer(cfg, port, host, authToken)
				return mongoServer.Start(browser)
			default:
				label := "🗄️  Starting SQL Studio"
				if strings.HasPrefix(strings.ToLower(url), "clickhouse://") {
					label = "🟡 Starting ClickHouse Studio"
				}
				fmt.Printf("%s: %s\n", label, maskDBURL(url))
				provider := detectProvider(url)
				cfg := &config.Config{
					Database: config.Database{
						Provider: provider,
						URLEnv:   "STUDIO_DB_URL",
					},
				}
				os.Setenv("STUDIO_DB_URL", url)
				server := studio.New(cfg, port, host, authToken)
				return server.Start(browser)
			}
		}

		// No URL provided — load from config as before
		cfg, err := loadConfigForDB(cmd)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("invalid config: %w", err)
		}

		if cfg.Database.Provider == "mongodb" || cfg.Database.Provider == "mongo" {
			fmt.Println("🍃 Starting MongoDB Studio...")
			mongoServer := mongodb.NewServer(cfg, port, host, authToken)
			return mongoServer.Start(browser)
		}

		if cfg.Database.Provider == "clickhouse" {
			fmt.Println("🟡 Starting ClickHouse Studio...")
		} else if cfg.Database.Provider == "scylla" || cfg.Database.Provider == "scylladb" || cfg.Database.Provider == "cassandra" {
			fmt.Println("🔵 Starting ScyllaDB Studio...")
		} else {
			fmt.Println("🗄️  Starting SQL Studio...")
		}
		server := studio.New(cfg, port, host, authToken)
		return server.Start(browser)
	},
}

func init() {
	studioCmd.Flags().IntP("port", "p", 5555, "Port to run studio on")
	studioCmd.Flags().BoolP("browser", "b", true, "Open browser automatically")
	studioCmd.Flags().String("host", "127.0.0.1", "Host to bind to (use 0.0.0.0 for all interfaces)")
	studioCmd.Flags().String("auth-token", "", "Bearer token for API authentication (required if host is 0.0.0.0)")
}

func maskDBURL(url string) string {
	if len(url) < 20 {
		return "***"
	}
	if idx := len(url) / 2; idx > 0 {
		return url[:10] + "***" + url[len(url)-10:]
	}
	return "***"
}

// detectStudioType determines which studio to launch based on the URL protocol.
func detectStudioType(url string) string {
	lower := strings.ToLower(url)
	if strings.HasPrefix(lower, "redis://") || strings.HasPrefix(lower, "rediss://") {
		return "redis"
	}
	if strings.HasPrefix(lower, "mongodb://") || strings.HasPrefix(lower, "mongodb+srv://") {
		return "mongodb"
	}
	return "sql"
}

func detectProvider(dbURL string) string {
	lower := strings.ToLower(dbURL)

	switch {
	case strings.HasPrefix(lower, "mongodb://") || strings.HasPrefix(lower, "mongodb+srv://"):
		return "mongodb"
	case strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://"):
		return "postgresql"
	case strings.HasPrefix(lower, "mysql://"):
		return "mysql"
	case strings.HasPrefix(lower, "sqlite://"):
		return "sqlite"
	case strings.HasPrefix(lower, "clickhouse://"):
		return "clickhouse"
	case strings.HasPrefix(lower, "scylla://") || strings.HasPrefix(lower, "cassandra://"):
		return "scylla"
	// http/https on port 8123 is the ClickHouse HTTP interface
	case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
		return "clickhouse"
	default:
		if strings.Contains(lower, "mongodb") {
			return "mongodb"
		} else if strings.Contains(lower, "postgres") {
			return "postgresql"
		} else if strings.Contains(lower, "clickhouse") {
			return "clickhouse"
		}
		return "postgresql"
	}
}
