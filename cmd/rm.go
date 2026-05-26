package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/devtime-ltd/slate/internal/compose"
	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/proxy"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var rmForce bool

var rmCmd = &cobra.Command{
	Use:   "rm <name>",
	Short: "Destroy workspace (containers, volumes, worktree)",
	Args:  cobra.ExactArgs(1),
	RunE:  runRm,
}

func init() {
	rmCmd.Flags().BoolVarP(&rmForce, "force", "f", false, "Skip confirmation prompt")
	rmCmd.GroupID = "workspace"
	rootCmd.AddCommand(rmCmd)
}

func runRm(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := workspace.ValidateName(name); err != nil {
		return err
	}

	wsDir, err := workspace.WorkspaceDir(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(wsDir); err != nil {
		return fmt.Errorf("workspace '%s' not found", name)
	}

	hostname, _ := workspace.Hostname(name)

	if !rmForce {
		fmt.Printf("This will destroy %s (containers, volumes, worktree). Continue? [y/N] ", hostname)
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(answer)) != "y" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	env, err := compose.NewEnv(name, wsDir)
	if err != nil {
		return err
	}

	fmt.Printf("Destroying %s...\n", hostname)
	compose.Run(env, "down", "-v")

	httpPort, httpsPort, tls := DetectProxyPorts()
	proxyConfig := config.WithPorts(httpPort, httpsPort, tls)
	proxy.Unregister(proxyConfig, hostname, []string{"vite", "mailpit"})

	workspace.RemoveWorktree(wsDir)

	fmt.Printf("✅ %s removed\n", hostname)
	return nil
}
