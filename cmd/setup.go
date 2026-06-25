package cmd

import (
	"fmt"

	"github.com/devtime-ltd/slate/internal/assets"
	"github.com/devtime-ltd/slate/internal/config"
	"github.com/spf13/cobra"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "One-time host setup (proxy + CA cert + secret key)",
	Long: `Sets up slate infrastructure on this machine:
  1. Generates a secret key for password derivation
  2. Starts the HTTPS proxy (Caddy container)
  3. Starts the *.test DNS resolver (dnsmasq container + /etc/resolver)
  4. Installs the CA certificate into the system trust store`,
	GroupID: "tools",
	RunE:    runSetup,
}

var teardownCmd = &cobra.Command{
	Use:   "teardown",
	Short: "Remove all slate infrastructure from this machine",
	Long: `Removes everything slate installed on the host:
  - Stops and removes the proxy container
  - Removes shared cache volumes
  - Removes the CA certificate file
  - Removes the global config

Does NOT remove workspaces or project files.`,
	GroupID: "tools",
	RunE:    runTeardown,
}

func init() {
	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(teardownCmd)
}

func runSetup(cmd *cobra.Command, args []string) error {
	fmt.Println("Setting up slate...")

	if _, err := assets.EnsureEntrypoint(); err != nil {
		return fmt.Errorf("writing entrypoint: %w", err)
	}
	fmt.Println("  " + tick() + " Entrypoint installed")

	secretKey, err := config.EnsureSecretKey()
	if err != nil {
		return fmt.Errorf("generating secret key: %w", err)
	}
	if secretKey != "" {
		fmt.Println("  " + tick() + " Secret key configured")
	}

	fmt.Println()
	if err := runProxyStart(cmd, args); err != nil {
		return err
	}

	fmt.Println()
	if err := setupDNS(); err != nil {
		fmt.Printf("  "+warn()+" DNS setup incomplete: %v\n", err)
		fmt.Println("  Retry with `slate dns start`.")
	}

	fmt.Println()
	if err := runProxyTrust(cmd, args); err != nil {
		fmt.Printf("  " + warn() + " CA trust failed: %v\n", err)
		fmt.Println("  Run `slate proxy trust` manually if needed.")
	}

	fmt.Println("\n" + tick() + " Setup complete. Run `slate init <scaffold>` in a project to get started.")
	return nil
}

func runTeardown(cmd *cobra.Command, args []string) error {
	fmt.Println("Removing slate infrastructure...")

	runProxyStop(cmd, args)
	fmt.Println("  " + tick() + " Proxy removed")

	teardownDNS()

	fmt.Println("  " + tick() + " Workspace caches are removed with `slate rm`")

	fmt.Println("\n" + tick() + " Teardown complete. Workspaces and project files are untouched.")
	return nil
}
