package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var agentCmd = &cobra.Command{
	Use:   "agent [workspace]",
	Short: "Run the project's agent command in a workspace",
	Long: `Runs the "agent:" command from the main checkout's slate.yml in the
workspace directory. Configure a single command, or a [first-run, thereafter]
pair; the first-run variant is picked on the workspace's first agent entry
(tracked via .slate/agent-started), whether that entry comes through the up
hook (SLATE_FRESH=1) or directly in a bare workspace.

Placeholders expanded: {{WORKSPACE}}, {{PROJECT}}, {{HOSTNAME}}.`,
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
		if cfg.Agent.IsZero() {
			return errors.New("no `agent:` in slate.yml; set a command (e.g. `agent: claude`) or a [first-run, thereafter] pair")
		}
		return runAgent(cfg, name, wsDir, agentFresh(wsDir))
	},
}

func agentStartedMarker(wsDir string) string {
	return filepath.Join(wsDir, ".slate", "agent-started")
}

// agentFresh decides whether this is the workspace's first agent entry.
// The agent-started marker is the source of truth once it exists: it stops
// a bare workspace's later `slate up` (which sets SLATE_FRESH=1) from
// re-running the first-run variant over a live session. Without a marker,
// SLATE_FRESH=1 (up hook) or a bare workspace means first entry; existing
// pre-marker workspaces fall through to the thereafter variant as before.
func agentFresh(wsDir string) bool {
	if _, err := os.Stat(agentStartedMarker(wsDir)); err == nil {
		return false
	}
	if os.Getenv("SLATE_FRESH") == "1" {
		return true
	}
	_, err := os.Stat(unprovisionedMarker(wsDir))
	return err == nil
}

func init() {
	rootCmd.AddCommand(agentCmd)
}

func runAgent(cfg config.ProjectConfig, wsName, wsDir string, fresh bool) error {
	command := cfg.Agent.Again
	if fresh {
		command = cfg.Agent.First
	}
	err := runHostCommand(cfg, command, wsName, wsDir, fresh)
	if err == nil {
		_ = os.WriteFile(agentStartedMarker(wsDir), nil, 0o644)
	}
	return err
}

func expandCommand(command, wsName, project string) string {
	return strings.NewReplacer(
		"{{WORKSPACE}}", wsName,
		"{{PROJECT}}", project,
		"{{HOSTNAME}}", workspace.HostnameForProject(project, wsName),
	).Replace(command)
}

// runHostCommand executes a slate.yml host command via sh -c in the workspace
// dir. Ordinary non-zero exits (ctrl-c, `exit 1`) aren't slate failures, but
// 126 and 127 mean the command itself couldn't run and must be surfaced.
func runHostCommand(cfg config.ProjectConfig, command, wsName, wsDir string, fresh bool) error {
	project, err := workspace.ProjectName(cfg.Project)
	if err != nil {
		return err
	}
	expanded := expandCommand(command, wsName, project)

	freshEnv := "0"
	if fresh {
		freshEnv = "1"
	}
	provisioningEnv := "0"
	if _, alive := readProvisioningLock(wsDir); alive {
		provisioningEnv = "1"
	}
	c := exec.Command("sh", "-c", expanded)
	c.Dir = wsDir
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = append(os.Environ(),
		"SLATE_WORKSPACE="+wsName,
		"SLATE_PROJECT="+project,
		"SLATE_FRESH="+freshEnv,
		"SLATE_PROVISIONING="+provisioningEnv,
	)
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if code := ee.ExitCode(); code == 126 || code == 127 {
				return fmt.Errorf("command failed (exit %d, not found/executable?): %s", code, expanded)
			}
			return nil
		}
		return err
	}
	return nil
}

// upAt is what new/up drop into after provisioning (behind the
// auto_cd/--cd gate): the up hook if configured, then a shell.
func upAt(cfg config.ProjectConfig, wsName, wsDir string, fresh bool) error {
	if cfg.Up != "" {
		if err := runHostCommand(cfg, cfg.Up, wsName, wsDir, fresh); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: %v\n", err)
		}
	}
	return spawnShellAt(wsDir)
}
