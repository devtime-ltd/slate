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
		if projectOverride != "" {
			path, err := resolveProjectPath(projectOverride)
			if err != nil {
				return err
			}
			workspace.SetMainRootOverride(path)
		}

		// Workspace target: --workspace/-w flag, else SLATE_WORKSPACE env.
		// (Scaffold tools disable flag parsing, so they also strip a leading
		// -w/--workspace from their args; the env var works everywhere.)
		ws := workspaceFlag
		if ws == "" {
			ws = os.Getenv("SLATE_WORKSPACE")
		}
		if ws != "" {
			workspace.SetWorkspaceOverride(ws)
		}
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
	rootCmd.PersistentFlags().StringVarP(&workspaceFlag, "workspace", "w", "", "Target a workspace by name instead of the CWD (or set SLATE_WORKSPACE)")

	doctorCmd.GroupID = "tools"
	initCmd.GroupID = "tools"

	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(initCmd)
}

var projectOverride string
var workspaceFlag string
