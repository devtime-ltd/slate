package cmd

import (
	"runtime"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var openCmd = &cobra.Command{
	Use:     "open <workspace>",
	Short:   "Open workspace URL in browser",
	Args:    requireWorkspaceName,
	GroupID: "tools",
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _, err := resolveNameOrCwd(args)
		if err != nil {
			return err
		}
		hostname, _ := workspace.Hostname(name)
		httpPort, httpsPort, tls := DetectProxyPorts()
		proxyConfig := config.WithPorts(httpPort, httpsPort, tls)
		url := proxyConfig.WorkspaceURL(hostname)

		opener := "xdg-open"
		if runtime.GOOS == "darwin" {
			opener = "open"
		}
		return hostCommand(opener, url).Run()
	},
}

func init() {
	rootCmd.AddCommand(openCmd)
}
