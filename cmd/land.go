package cmd

import (
	"bufio"
	"errors"
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

A landing_cmd runs on your host, so the first use per project (and any time
the command changes) asks for confirmation; consent is remembered outside
the repo. Placeholders expanded: {{WORKSPACE}}, {{PROJECT}}, {{HOSTNAME}}.`,
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
		command, ok := cfg.LandingCommand()
		if !ok {
			return spawnShellAt(wsDir)
		}
		if cfg.LandingCmd != "" {
			approved, err := approveLandingCmd(mainRoot, command)
			if err != nil {
				return err
			}
			if !approved {
				fmt.Println("Cancelled.")
				return nil
			}
		}
		return runLanding(cfg, command, name, wsDir)
	},
}

func init() {
	rootCmd.AddCommand(landCmd)
}

// approveLandingCmd gates repo-supplied landing_cmd values: they execute on
// the host, so each project + command string needs one-time consent.
func approveLandingCmd(mainRoot, command string) (bool, error) {
	if config.LandingApproved(mainRoot, command) {
		return true, nil
	}
	if !isInteractiveTerminal() {
		return false, fmt.Errorf("landing_cmd not yet approved for this project; run `slate land` interactively once to approve:\n  %s", command)
	}
	fmt.Printf("This project's slate.yml wants to run on your host:\n\n  %s\n\nRun it now and remember for this project? [y/N] ", command)
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(answer)) != "y" {
		return false, nil
	}
	if err := config.ApproveLanding(mainRoot, command); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not persist approval: %v\n", err)
	}
	return true, nil
}

func expandLanding(command, wsName, project string) string {
	return strings.NewReplacer(
		"{{WORKSPACE}}", wsName,
		"{{PROJECT}}", project,
		"{{HOSTNAME}}", workspace.HostnameForProject(project, wsName),
	).Replace(command)
}

// runLanding executes the landing command via sh -c in the workspace dir.
// Ordinary non-zero exits (ctrl-c, `exit 1`) aren't slate failures, but 126
// and 127 mean the command itself couldn't run and must be surfaced.
func runLanding(cfg config.ProjectConfig, command, wsName, wsDir string) error {
	project, err := workspace.ProjectName(cfg.Project)
	if err != nil {
		return err
	}
	expanded := expandLanding(command, wsName, project)

	c := exec.Command("sh", "-c", expanded)
	c.Dir = wsDir
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = append(os.Environ(), "SLATE_WORKSPACE="+wsName)
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if code := ee.ExitCode(); code == 126 || code == 127 {
				return fmt.Errorf("landing command failed (exit %d, not found/executable?): %s", code, expanded)
			}
			return nil
		}
		return err
	}
	return nil
}

// landAt is what new/up drop into after provisioning (behind the
// auto_cd/--cd gate): the landing command if configured, then a shell.
func landAt(cfg config.ProjectConfig, mainRoot, wsName, wsDir string) error {
	if cfg.Landing == "none" {
		return nil
	}
	if command, ok := cfg.LandingCommand(); ok {
		run := true
		if cfg.LandingCmd != "" {
			approved, err := approveLandingCmd(mainRoot, command)
			if err != nil {
				fmt.Fprintln(os.Stderr, "  "+err.Error())
			}
			run = approved
		}
		if run {
			if err := runLanding(cfg, command, wsName, wsDir); err != nil {
				fmt.Fprintf(os.Stderr, "  warning: %v\n", err)
			}
		}
	}
	return spawnShellAt(wsDir)
}
