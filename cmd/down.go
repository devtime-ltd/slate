package cmd

import (
	"fmt"

	"github.com/devtime-ltd/slate/internal/compose"
	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/proxy"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var downCmd = &cobra.Command{
	Use:   "down [name]",
	Short: "Stop a workspace (preserves data volume)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runDown,
}

func init() {
	downCmd.GroupID = "workspace"
	rootCmd.AddCommand(downCmd)
}

func runDown(cmd *cobra.Command, args []string) error {
	name, wsDir, err := resolveNameOrCwd(args)
	if err != nil {
		return err
	}

	env, err := compose.NewEnv(name, wsDir)
	if err != nil {
		return err
	}

	hostname, _ := workspace.Hostname(name)
	fmt.Printf("Stopping %s...\n", hostname)
	compose.Run(env, "down")

	httpPort, httpsPort, tls := DetectProxyPorts()
	proxyConfig := config.WithPorts(httpPort, httpsPort, tls)
	proxy.Unregister(proxyConfig, hostname, []string{"vite", "mailpit"})

	fmt.Printf("✅ %s is down\n", hostname)
	return nil
}
