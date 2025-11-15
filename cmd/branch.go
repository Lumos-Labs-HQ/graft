package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/Lumos-Labs-HQ/flash/internal/branch"
	"github.com/Lumos-Labs-HQ/flash/internal/config"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var branchCmd = &cobra.Command{
	Use:   "branch [name]",
	Short: "List, create, or delete branches",
	Long:  `List, create, or delete branches. Similar to git branch.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		manager, err := branch.NewManager(cfg)
		if err != nil {
			return err
		}
		defer manager.Close()

		// Handle delete flag
		deleteBranch, _ := cmd.Flags().GetString("delete")
		if deleteBranch != "" {
			return handleDeleteBranch(manager, deleteBranch, cmd)
		}

		// Handle rename flag
		renameTo, _ := cmd.Flags().GetString("move")
		if renameTo != "" {
			if len(args) == 0 {
				return fmt.Errorf("branch name required for rename")
			}
			return handleRenameBranch(manager, renameTo, args[0])
		}

		// Create branch if name provided
		if len(args) > 0 {
			return handleCreateBranch(manager, args[0], cmd)
		}

		// List branches (default)
		return handleListBranches(manager)
	},
}

var checkoutCmd = &cobra.Command{
	Use:   "checkout <branch>",
	Short: "Switch branches or create and switch",
	Long:  `Switch to a branch. Use -b to create and switch. Similar to git checkout.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		branchName := args[0]

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		manager, err := branch.NewManager(cfg)
		if err != nil {
			return err
		}
		defer manager.Close()

		createNew, _ := cmd.Flags().GetBool("b")
		
		if createNew {
			// Create and switch
			if err := handleCreateBranch(manager, branchName, cmd); err != nil {
				return err
			}
		}

		// Switch to branch
		ctx := context.Background()
		if err := manager.SwitchBranch(ctx, branchName); err != nil {
			return fmt.Errorf("failed to switch branch: %w", err)
		}

		color.Green("✓ Switched to branch '%s'", branchName)
		return nil
	},
}

func handleCreateBranch(manager *branch.Manager, branchName string, cmd *cobra.Command) error {
	currentBranch, err := manager.GetCurrentBranch()
	if err != nil {
		return err
	}

	force, _ := cmd.Flags().GetBool("force")
	if !force {
		color.Yellow("⚠️  This will copy all schema and data from '%s' to '%s'.", currentBranch, branchName)
		fmt.Print("Continue? (y/N): ")
		var response string
		fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			color.Red("✗ Cancelled")
			return nil
		}
	}

	color.Cyan("Creating branch '%s'...", branchName)
	
	ctx := context.Background()
	if err := manager.CreateBranch(ctx, branchName); err != nil {
		return fmt.Errorf("failed to create branch: %w", err)
	}

	color.Green("✓ Branch '%s' created successfully", branchName)
	return nil
}

func handleDeleteBranch(manager *branch.Manager, branchName string, cmd *cobra.Command) error {
	force, _ := cmd.Flags().GetBool("force")
	
	current, err := manager.GetCurrentBranch()
	if err != nil {
		return err
	}

	if current == branchName {
		return fmt.Errorf("cannot delete the current branch '%s'", branchName)
	}

	if !force {
		color.Yellow("⚠️  This will permanently delete branch '%s'.", branchName)
		fmt.Print("Continue? (y/N): ")
		var response string
		fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			color.Red("✗ Cancelled")
			return nil
		}
	}

	ctx := context.Background()
	if err := manager.DeleteBranch(ctx, branchName); err != nil {
		return fmt.Errorf("failed to delete branch: %w", err)
	}

	color.Green("✓ Branch '%s' deleted successfully", branchName)
	return nil
}

func handleRenameBranch(manager *branch.Manager, oldName, newName string) error {
	if err := manager.RenameBranch(oldName, newName); err != nil {
		return fmt.Errorf("failed to rename branch: %w", err)
	}

	color.Green("✓ Branch '%s' renamed to '%s'", oldName, newName)
	return nil
}

func handleListBranches(manager *branch.Manager) error {
	branches, current, err := manager.ListBranches()
	if err != nil {
		return err
	}

	if len(branches) == 0 {
		color.Yellow("No branches found")
		return nil
	}

	fmt.Println()
	for _, b := range branches {
		prefix := "  "
		if b.Name == current {
			prefix = color.GreenString("* ")
		}

		status := ""
		if b.IsDefault {
			status = color.CyanString(" (default)")
		} else if b.Name == current {
			status = color.GreenString(" (active)")
		}

		age := time.Since(b.CreatedAt)
		ageStr := formatDuration(age)

		fmt.Printf("%s%-15s %s - Created %s ago\n", prefix, b.Name, status, ageStr)
	}
	fmt.Println()

	return nil
}

var branchDiffCmd = &cobra.Command{
	Use:   "diff <branch1> <branch2>",
	Short: "Show schema differences between two branches",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		branch1 := args[0]
		branch2 := args[1]

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		manager, err := branch.NewManager(cfg)
		if err != nil {
			return err
		}
		defer manager.Close()

		ctx := context.Background()
		diff, err := manager.GetSchemaDiff(ctx, branch1, branch2)
		if err != nil {
			return fmt.Errorf("failed to get schema diff: %w", err)
		}

		if diff.IsEmpty() {
			color.Green("✓ No differences found between '%s' and '%s'", branch1, branch2)
			return nil
		}

		color.Cyan("\nSchema differences between '%s' and '%s':\n", branch1, branch2)
		fmt.Println(diff.String())

		return nil
	},
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", mins)
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	}
	days := int(d.Hours() / 24)
	if days == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", days)
}

func init() {
	rootCmd.AddCommand(branchCmd)
	rootCmd.AddCommand(checkoutCmd)
	
	branchCmd.AddCommand(branchDiffCmd)

	// Branch command flags
	branchCmd.Flags().StringP("delete", "d", "", "Delete a branch")
	branchCmd.Flags().StringP("move", "m", "", "Rename a branch")
	branchCmd.Flags().BoolP("force", "f", false, "Skip confirmation prompt")

	// Checkout command flags
	checkoutCmd.Flags().BoolP("b", "b", false, "Create a new branch and switch to it")
	checkoutCmd.Flags().BoolP("force", "f", false, "Skip confirmation prompt")
}
