package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var landCmd = &cobra.Command{
	Use:   "land [workspace]",
	Short: "Run the project's landing command in a workspace",
	Long: `Runs the configured landing (a "landing:" preset or "landing_cmd:") in the
workspace directory: the same thing slate new/up drop you into when ready.
With no landing command configured, spawns a shell there instead.

Placeholders expanded in landing_cmd: {{WORKSPACE}}, {{PROJECT}}, {{HOSTNAME}}.`,
	GroupID: "tools",
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, wsDir, err := resolveNameOrCwd(args)
		if err != nil {
			return err
		}
		mainRoot, err := workspace.MainRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadProjectForWorkspace(mainRoot, wsDir)
		if err != nil {
			return err
		}
		warnIfWorkspaceConfigDiffers(mainRoot, wsDir)
		if command, ok := cfg.LandingCommand(); ok {
			return runLanding(command, name, wsDir)
		}
		return spawnShellAt(wsDir)
	},
}

func init() {
	rootCmd.AddCommand(landCmd)
}

func expandLanding(command, wsName, hostname string) string {
	project := strings.TrimSuffix(hostname, "--"+wsName)
	return strings.NewReplacer(
		"{{WORKSPACE}}", wsName,
		"{{PROJECT}}", project,
		"{{HOSTNAME}}", hostname,
	).Replace(command)
}

// runLanding executes the landing command via sh -c in the workspace dir.
// Like spawnShellAt, a non-zero exit isn't a slate failure.
func runLanding(command, wsName, wsDir string) error {
	hostname, err := resolveHostname(wsName)
	if err != nil {
		return err
	}
	c := exec.Command("sh", "-c", expandLanding(command, wsName, hostname))
	c.Dir = wsDir
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = append(os.Environ(), "SLATE_WORKSPACE="+wsName)
	if err := c.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return nil
		}
		return err
	}
	return nil
}

// landAt is what new/up drop into after provisioning (behind the
// auto_cd/--cd gate): the landing command if configured, then a shell.
func landAt(cfg config.ProjectConfig, wsName, wsDir string) error {
	if cfg.Landing == "none" {
		return nil
	}
	if command, ok := cfg.LandingCommand(); ok {
		if err := runLanding(command, wsName, wsDir); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: landing command failed: %v\n", err)
		}
	}
	return spawnShellAt(wsDir)
}
