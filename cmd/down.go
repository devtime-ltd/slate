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
	if err := requireDocker(); err != nil {
		return err
	}
	name, wsDir, err := resolveNameOrCwd(args)
	if err != nil {
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

	fmt.Printf("Stopping %s...\n", hostname)
	compose.Run(env, "down", "--remove-orphans")

	mainRoot, _ := workspace.MainRoot()
	cfg, _ := config.LoadProject(mainRoot)
	proxy.Unregister(hostname, scaffoldSubdomains(cfg))

	fmt.Printf(""+tick()+" %s is down\n", hostname)
	return nil
}
