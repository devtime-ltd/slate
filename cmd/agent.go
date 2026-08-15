package cmd

import (
	"bufio"
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
(tracked via .slate/agent-pending and .slate/agent-started), whether that
entry comes through the up hook (SLATE_FRESH=1) or directly.

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

func agentPendingMarker(wsDir string) string {
	return filepath.Join(wsDir, ".slate", "agent-pending")
}

// agentFresh decides whether this is the workspace's first agent entry.
// The agent-started marker is the source of truth once it exists: it stops
// a bare workspace's later `slate up` (which sets SLATE_FRESH=1) from
// re-running the first-run variant over a live session. Before it, the
// agent-pending marker (written at creation, cleared after the first
// session) means first entry however the workspace was provisioned - a
// non-interactive provision fires no up hook, so there is no SLATE_FRESH=1
// entry to catch. SLATE_FRESH=1 and the bare unprovisioned marker remain
// for workspaces created before the pending marker existed; anything else
// falls through to the thereafter variant as before.
func agentFresh(wsDir string) bool {
	if _, err := os.Stat(agentStartedMarker(wsDir)); err == nil {
		return false
	}
	if _, err := os.Stat(agentPendingMarker(wsDir)); err == nil {
		return true
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
	err := runHostCommand(cfg, command, wsName, wsDir, fresh, "SLATE_AGENT=1")
	if err == nil {
		_ = os.WriteFile(agentStartedMarker(wsDir), nil, 0o644)
		_ = os.Remove(agentPendingMarker(wsDir))
		offerTeardownOnExit(wsName, wsDir)
	}
	return err
}

// offerTeardownOnExit runs after an agent session ends. It speaks only when
// there is something worth saying: a teardown staged from inside the session
// (via `slate done`), or work that provably landed. Mid-work exits stay
// silent, and nothing is destroyed without a human saying so - except a
// staged teardown, which the human already asked for in-session. A declined
// offer is remembered per tip so re-entering the session doesn't nag.
func offerTeardownOnExit(wsName, wsDir string) {
	mainRoot, err := workspace.MainRoot()
	if err != nil {
		return
	}
	ev := checkLanded(mainRoot, wsDir)

	// A staged marker only counts for the tip it was staged at: a marker
	// left behind by an earlier incarnation of this workspace name, or
	// staged before further commits, must not authorise destroying the
	// current state.
	staged, stale := false, false
	if raw, err := os.ReadFile(stagedTeardownMarker(wsDir)); err == nil {
		if strings.TrimSpace(string(raw)) == ev.tip && ev.tip != "" {
			staged = true
		} else {
			stale = true
			_ = os.Remove(stagedTeardownMarker(wsDir))
		}
	}

	interactive := isInteractiveTerminal()
	hostname, err := resolveHostname(wsName)
	if err != nil {
		return
	}

	switch {
	case staged && ev.ok:
		fmt.Printf("Teardown was staged in this session (%s).\n", ev.evidence)
		if err := destroyWorkspace(wsName, wsDir, hostname, false, ev.branchOverride(), false, &ev); err != nil {
			fmt.Fprintf(os.Stderr, "warning: staged teardown failed: %v\n", err)
		}
	case staged:
		// The workspace changed between staging and exit; don't destroy
		// work on the strength of a stale request.
		_ = os.Remove(stagedTeardownMarker(wsDir))
		fmt.Printf("Teardown was staged in this session, but %s is no longer safe to remove:\n", wsName)
		for _, r := range ev.reasons {
			fmt.Printf("  - %s\n", r)
		}
		fmt.Println("Staging cleared; run `slate done` again once the work has landed.")
	case stale:
		fmt.Printf("A staged teardown for %s no longer matches the workspace's state; staging cleared - run `slate done` again once ready.\n", wsName)
	case ev.ok && ev.hasWork && interactive:
		if declined, _ := os.ReadFile(teardownDeclinedMarker(wsDir)); strings.TrimSpace(string(declined)) == ev.tip {
			return // already asked about exactly this state; don't nag
		}
		fmt.Printf("Work landed (%s). Tear down %s? [y/N] ", ev.evidence, wsName)
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(answer)) != "y" {
			_ = createMarkerFile(teardownDeclinedMarker(wsDir), []byte(ev.tip+"\n"))
			return
		}
		if err := destroyWorkspace(wsName, wsDir, hostname, false, ev.branchOverride(), false, &ev); err != nil {
			fmt.Fprintf(os.Stderr, "warning: teardown failed: %v\n", err)
		}
	}
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
func runHostCommand(cfg config.ProjectConfig, command, wsName, wsDir string, fresh bool, extraEnv ...string) error {
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
	c.Env = append(c.Env, extraEnv...)
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
	return spawnShellUnlessFinished(wsDir)
}
