package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/workspace"
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
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if projectOverride == "" {
			return nil
		}
		path, err := resolveProjectPath(projectOverride)
		if err != nil {
			return err
		}
		workspace.SetMainRootOverride(path)
		return nil
	}

	registerToolCommands()
	return rootCmd.Execute()
}

func resolveProjectPath(nameOrPath string) (string, error) {
	if filepath.IsAbs(nameOrPath) {
		if _, err := os.Stat(nameOrPath); err != nil {
			return "", fmt.Errorf("project path not found: %s", nameOrPath)
		}
		return nameOrPath, nil
	}
	if path, ok := config.ProjectsByName()[nameOrPath]; ok {
		return path, nil
	}
	return "", fmt.Errorf("project '%s' not registered. Run `slate ls --all` to see known projects", nameOrPath)
}

func init() {
	rootCmd.AddGroup(groupWorkspace, groupTools, groupScaffold)
	rootCmd.PersistentFlags().StringVar(&projectOverride, "project", "", "Target a specific project (name from registry or absolute path)")

	doctorCmd.GroupID = "tools"
	initCmd.GroupID = "tools"

	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(initCmd)
}

var projectOverride string
