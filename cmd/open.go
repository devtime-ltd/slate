package cmd

import (
	"runtime"

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
		hostname, err := resolveHostname(name)
		if err != nil {
			return err
		}
		proxyConfig, _ := loadProxyConfig(false)
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
