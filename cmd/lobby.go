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

var lobbyCmd = &cobra.Command{
	Use:   "lobby [workspace]",
	Short: "Run the project's lobby command in a workspace",
	Long: `Runs the configured lobby (a "lobby:" preset or "lobby_cmd:") in the
workspace directory: the same thing slate new/up drop you into when ready.
With no lobby command configured, spawns a shell there instead.

A lobby_cmd runs on your host, so the first use per project (and any time
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
		command, ok := cfg.LobbyCommand()
		if !ok {
			return spawnShellAt(wsDir)
		}
		if cfg.LobbyCmd != "" {
			approved, err := approveLobbyCmd(mainRoot, command)
			if err != nil {
				return err
			}
			if !approved {
				fmt.Println("Cancelled.")
				return nil
			}
		}
		return runLobby(cfg, command, name, wsDir)
	},
}

func init() {
	rootCmd.AddCommand(lobbyCmd)
}

// approveLobbyCmd gates repo-supplied lobby_cmd values: they execute on
// the host, so each project + command string needs one-time consent.
func approveLobbyCmd(mainRoot, command string) (bool, error) {
	if config.LobbyApproved(mainRoot, command) {
		return true, nil
	}
	if !isInteractiveTerminal() {
		return false, fmt.Errorf("lobby_cmd not yet approved for this project; run `slate lobby` interactively once to approve:\n  %s", command)
	}
	fmt.Printf("This project's slate.yml wants to run on your host:\n\n  %s\n\nRun it now and remember for this project? [y/N] ", command)
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(answer)) != "y" {
		return false, nil
	}
	if err := config.ApproveLobby(mainRoot, command); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not persist approval: %v\n", err)
	}
	return true, nil
}

func expandLobby(command, wsName, project string) string {
	return strings.NewReplacer(
		"{{WORKSPACE}}", wsName,
		"{{PROJECT}}", project,
		"{{HOSTNAME}}", workspace.HostnameForProject(project, wsName),
	).Replace(command)
}

// runLobby executes the lobby command via sh -c in the workspace dir.
// Ordinary non-zero exits (ctrl-c, `exit 1`) aren't slate failures, but 126
// and 127 mean the command itself couldn't run and must be surfaced.
func runLobby(cfg config.ProjectConfig, command, wsName, wsDir string) error {
	project, err := workspace.ProjectName(cfg.Project)
	if err != nil {
		return err
	}
	expanded := expandLobby(command, wsName, project)

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
				return fmt.Errorf("lobby command failed (exit %d, not found/executable?): %s", code, expanded)
			}
			return nil
		}
		return err
	}
	return nil
}

// lobbyAt is what new/up drop into after provisioning (behind the
// auto_cd/--cd gate): the lobby command if configured, then a shell.
func lobbyAt(cfg config.ProjectConfig, mainRoot, wsName, wsDir string) error {
	if cfg.Lobby == "none" {
		return nil
	}
	if command, ok := cfg.LobbyCommand(); ok {
		run := true
		if cfg.LobbyCmd != "" {
			approved, err := approveLobbyCmd(mainRoot, command)
			if err != nil {
				fmt.Fprintln(os.Stderr, "  "+err.Error())
			}
			run = approved
		}
		if run {
			if err := runLobby(cfg, command, wsName, wsDir); err != nil {
				fmt.Fprintf(os.Stderr, "  warning: %v\n", err)
			}
		}
	}
	return spawnShellAt(wsDir)
}
