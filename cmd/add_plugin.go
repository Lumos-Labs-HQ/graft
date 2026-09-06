package cmd

import (
	"fmt"
	"runtime"
	"slices"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/Lumos-Labs-HQ/flash/internal/plugin"
)

var addPluginCmd = &cobra.Command{
	Use:   "add-plug [plugin-name]",
	Short: "Install a FlashORM plugin",
	Long: `
Install a FlashORM plugin to add additional functionality.

Available plugins:
  • core    - Core ORM features (auto-installed on first use, but can be installed manually)
  • studio  - Visual database editor (optional, install separately)

Examples:
  flash add-plug core             # Install latest stable version
  flash add-plug studio           # Install studio plugin
  flash add-plug core@beta        # Install latest beta version
  flash add-plug core@1.0.0       # Install specific version`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		pluginSpec := args[0]

		parts := strings.Split(pluginSpec, "@")
		pluginName := parts[0]
		version := "latest"
		if len(parts) > 1 {
			version = parts[1]
		}

		availablePlugins := plugin.GetAllPlugins()
		valid := slices.Contains(availablePlugins, pluginName)

		if !valid {
			color.Red("❌ Unknown plugin: %s", pluginName)
			fmt.Println()
			color.White("Available plugins:")
			for _, name := range availablePlugins {
				fmt.Printf("  • %s - %s\n", color.GreenString(name), plugin.GetPluginDescription(name))
			}
			return fmt.Errorf("invalid plugin name")
		}

		manager, err := plugin.NewManager()
		if err != nil {
			return fmt.Errorf("failed to initialize plugin manager: %w", err)
		}

		if err := manager.InstallPlugin(pluginName, version); err != nil {
			color.Red("❌ Installation failed: %v", err)
			fmt.Println()

			if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found") {
				color.Yellow("💡 Suggestions:")
				color.White("   • Check if the plugin is built for your platform (%s/%s)", runtime.GOOS, runtime.GOARCH)
				color.White("   • Verify the release exists on GitHub")
				color.White("   • Try: flash plugins --online")
			}

			return fmt.Errorf("failed to install plugin: %w", err)
		}

		return nil
	},
}
