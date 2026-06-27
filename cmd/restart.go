package cmd

import (
	"fmt"

	"github.com/devtime-ltd/slate/internal/compose"
	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/proxy"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var restartCmd = &cobra.Command{
	Use:   "restart [workspace] [service]",
	Short: "Restart a workspace or a single service",
	Long: `Restarts all services in a workspace, or a single service if specified.
Re-registers the HTTPS proxy in case ports changed.

Service names depend on the scaffold (e.g. app, queue, mysql, vite, mailpit).`,
	Args:    cobra.MaximumNArgs(2),
	GroupID: "workspace",
	RunE:    runRestart,
}

func init() {
	rootCmd.AddCommand(restartCmd)
}

func runRestart(cmd *cobra.Command, args []string) error {
	if err := requireDocker(); err != nil {
		return err
	}
	name, err := resolveWorkspaceArg(args)
	if err != nil {
		return err
	}
	if err := workspace.ValidateName(name); err != nil {
		return err
	}

	wsDir, err := workspace.WorkspaceDir(name)
	if err != nil {
		return err
	}
	if err := checkNotProvisioning(wsDir); err != nil {
		return err
	}

	hostname, err := resolveHostname(name)
	if err != nil {
		return err
	}

	env, err := compose.NewEnv(name, wsDir, hostname)
	if err != nil {
		return err
	}

	if len(args) > 1 {
		service := args[1]
		fmt.Printf("Restarting %s in %s...\n", service, hostname)
		if err := compose.Run(env, "restart", service); err != nil {
			return fmt.Errorf("restart failed: %w", err)
		}
	} else {
		fmt.Printf("Restarting %s...\n", hostname)
		if err := compose.Run(env, "restart"); err != nil {
			return fmt.Errorf("restart failed: %w", err)
		}
	}

	mainRoot, _ := workspace.MainRoot()
	cfg, _ := config.LoadProject(mainRoot)

	services := buildServicePorts(env, cfg)
	if err := proxy.Register(hostname, services); err != nil {
		fmt.Printf("  warning: proxy re-registration failed: %v\n", err)
	}

	proxyConfig, _ := loadProxyConfig(false)

	fmt.Println()
	fmt.Println(tick() + " " + hostname + " restarted")
	fmt.Println()
	fmt.Println(workspaceURLBlock(env, hostname, cfg, proxyConfig))
	return nil
}
