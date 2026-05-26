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
	Use:   "restart <workspace> [service]",
	Short: "Restart a workspace or a single service",
	Long: `Restarts all services in a workspace, or a single service if specified.
Re-registers the HTTPS proxy in case ports changed.

Service names depend on the scaffold (e.g. app, queue, mysql, vite, mailpit).`,
	Args:    cobra.RangeArgs(1, 2),
	GroupID: "workspace",
	RunE:    runRestart,
}

func init() {
	rootCmd.AddCommand(restartCmd)
}

func runRestart(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := workspace.ValidateName(name); err != nil {
		return err
	}

	wsDir, err := workspace.WorkspaceDir(name)
	if err != nil {
		return err
	}

	env, err := compose.NewEnv(name, wsDir)
	if err != nil {
		return err
	}

	hostname, _ := workspace.Hostname(name)

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

	httpPort, httpsPort, tls := DetectProxyPorts()
	proxyConfig := config.WithPorts(httpPort, httpsPort, tls)

	services := buildServicePorts(env, cfg)
	if err := proxy.Register(proxyConfig, hostname, services); err != nil {
		fmt.Printf("  warning: proxy re-registration failed: %v\n", err)
	}

	fmt.Printf("✅ %s restarted\n", hostname)
	return nil
}
