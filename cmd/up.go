package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/devtime-ltd/slate/internal/compose"
	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/scaffold"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var upBuild bool
var upFresh bool
var upBg bool
var upCd bool

var upCmd = &cobra.Command{
	Use:   "up [name]",
	Short: "Start/refresh a workspace (idempotent)",
	Long:  "Brings containers up, refreshes deps, runs migrations. Safe to re-run.",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runUp,
}

func init() {
	upCmd.Flags().BoolVar(&upBuild, "build", false, "Force image rebuild")
	upCmd.Flags().BoolVar(&upFresh, "fresh", false, "Recreate containers + volumes (worktree code preserved)")
	upCmd.Flags().BoolVar(&upBg, "bg", false, "Run container build + lifecycle in the background")
	upCmd.Flags().BoolVar(&upCd, "cd", false, "Spawn a shell in the workspace directory (default from global auto_cd; pass --cd=false to opt out)")
	upCmd.GroupID = "workspace"
	rootCmd.AddCommand(upCmd)
}

func runUp(cmd *cobra.Command, args []string) error {
	if err := requireDocker(); err != nil {
		return err
	}
	cd := resolveAutoCd(cmd, "cd", upCd)
	name, wsDir, err := resolveNameOrCwd(args)
	if err == nil {
		if err := checkNotProvisioning(wsDir); err != nil {
			return err
		}
	}
	if err != nil {
		// Only offer to create when the failure is "not found", not validation
		if len(args) > 0 && strings.Contains(err.Error(), "not found") {
			fmt.Printf("Workspace '%s' doesn't exist. Create it? [Y/n] ", args[0])
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			answer = strings.TrimSpace(strings.ToLower(answer))
			if answer == "" || answer == "y" {
				return createWorkspace(args[0], "", upBg, cd)
			}
			return fmt.Errorf("workspace '%s' not found", args[0])
		}
		return err
	}

	mainRoot, err := workspace.MainRoot()
	if err != nil {
		return err
	}

	cfg, err := config.LoadProject(mainRoot)
	if err != nil {
		return fmt.Errorf("loading slate.yml: %w", err)
	}

	if err := scaffold.Generate(wsDir, cfg); err != nil {
		return fmt.Errorf("generating scaffold: %w", err)
	}
	if s, err := scaffold.Get(cfg.Scaffold); err == nil {
		if err := scaffold.GenerateFileMounts(wsDir, cfg, s); err != nil {
			return fmt.Errorf("generating file mounts: %w", err)
		}
	}

	projectName, err := workspace.ProjectName(cfg.Project)
	if err != nil {
		return err
	}
	hostname := workspace.HostnameForProject(projectName, name)

	proxyConfig, err := loadProxyConfig(true)
	if err != nil {
		return err
	}

	if err := scaffold.GenerateEnvContainer(wsDir, hostname, projectName, name, cfg, proxyConfig); err != nil {
		return fmt.Errorf("generating .env.container: %w", err)
	}

	opts := provisionOpts{fresh: upFresh, build: upBuild, wipe: upFresh}

	if upBg {
		return runBackgroundProvision(name, wsDir, opts, cd)
	}

	env, err := compose.NewEnv(name, wsDir, hostname)
	if err != nil {
		return err
	}
	if err := runWorkspaceLifecycle(env, name, wsDir, hostname, cfg, proxyConfig, opts); err != nil {
		return err
	}

	if cd {
		return cdIntoWorkspace(wsDir)
	}
	return nil
}

func resolveNameOrCwd(args []string) (string, string, error) {
	if len(args) > 0 {
		name := args[0]
		if err := workspace.ValidateName(name); err != nil {
			return "", "", err
		}
		dir, err := workspace.WorkspaceDir(name)
		if err != nil {
			return "", "", err
		}
		if _, err := os.Stat(dir); err != nil {
			return "", "", fmt.Errorf("workspace '%s' not found", name)
		}
		return name, dir, nil
	}
	if name, dir, err := workspace.ResolveFromCwd(); err == nil {
		return name, dir, nil
	}
	// CWD isn't a workspace; prompt the user to pick from the project.
	name, err := pickWorkspace()
	if err != nil {
		return "", "", err
	}
	dir, err := workspace.WorkspaceDir(name)
	if err != nil {
		return "", "", err
	}
	return name, dir, nil
}
