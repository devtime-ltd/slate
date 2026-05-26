package cmd

import (
	"fmt"
	"os"

	"github.com/devtime-ltd/slate/internal/assets"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:          "slate",
	Short:        "Containerised dev workspaces with HTTPS",
	SilenceUsage: true,
	Long: `Slate creates isolated, ephemeral dev workspaces using Docker containers
and git worktrees. Each workspace gets its own containers, database, and
HTTPS URL.

Quick start:
  slate init laravel      Set up slate for a Laravel project
  slate new my-feature    Create a workspace
  slate up                Start/refresh the current workspace
  slate down              Stop (preserves data)
  slate rm my-feature     Destroy everything`,
}

var (
	groupWorkspace = &cobra.Group{ID: "workspace", Title: "Workspace lifecycle:"}
	groupTools     = &cobra.Group{ID: "tools", Title: "Tools:"}
	groupScaffold  = &cobra.Group{ID: "scaffold", Title: "Scaffold tools (from slate.yml):"}
)

func Execute() error {
	if _, err := assets.EnsureEntrypoint(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write entrypoint: %v\n", err)
	}
	registerToolCommands()
	return rootCmd.Execute()
}

func init() {
	rootCmd.AddGroup(groupWorkspace, groupTools, groupScaffold)

	doctorCmd.GroupID = "tools"
	initCmd.GroupID = "tools"

	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(initCmd)
}
