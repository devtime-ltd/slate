package cmd

import (
	"fmt"

	"github.com/devtime-ltd/slate/internal/compose"
	"github.com/devtime-ltd/slate/internal/proxy"
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

	proxy.UnregisterAll(hostname)

	fmt.Printf(""+tick()+" %s is down\n", hostname)
	fmt.Printf("  Bring it back with `slate up %s`, or destroy it with `slate rm %s` if it's no longer needed.\n", name, name)
	return nil
}
