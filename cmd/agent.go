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
	Use:   "agent [workspace] [-- args...]",
	Short: "Run the project's agent command in a workspace",
	Long: `Runs the "agent:" command from the main checkout's slate.yml in the
workspace directory. Configure a single command, or a [first-run, thereafter]
pair; the first-run variant is picked on the workspace's first agent entry
(tracked via .slate/agent-pending and .slate/agent-started), whether that
entry comes through the up hook (SLATE_FRESH=1) or directly.

Anything after -- is appended to the configured command, so a caller can add
its own flags or a prompt without duplicating the command in slate.yml.

Placeholders expanded: {{WORKSPACE}}, {{PROJECT}}, {{HOSTNAME}}.`,
	GroupID: "tools",
	Args:    cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		target, extra := splitAtDash(cmd, args)
		if len(target) > 1 {
			return fmt.Errorf("accepts at most one workspace, got %d (put agent arguments after --)", len(target))
		}
		// Ahead of resolving the workspace, which may fall back to the
		// interactive picker: without a terminal there is nothing to pick
		// with, and nothing worth picking for.
		if !isInteractiveTerminal() && !hooksOptIn() {
			return errors.New("`slate agent` needs a terminal: the configured agent command is interactive and would exit immediately without one.\nRun it from a terminal or inside a tmux session, or set SLATE_HOOKS=1 to run it anyway")
		}
		name, wsDir, err := resolveNameOrCwd(target)
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
		return runAgent(cfg, name, wsDir, agentFresh(wsDir), extra)
	},
}

// splitAtDash separates the command's own arguments from everything after a
// literal --, which is passed through to the configured agent command.
func splitAtDash(cmd *cobra.Command, args []string) ([]string, []string) {
	i := cmd.ArgsLenAtDash()
	if i < 0 {
		return args, nil
	}
	return args[:i], args[i:]
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

func runAgent(cfg config.ProjectConfig, wsName, wsDir string, fresh bool, extraArgs []string) error {
	command := cfg.Agent.Again
	if fresh {
		command = cfg.Agent.First
	}
	err := runHostCommand(cfg, command, wsName, wsDir, fresh, hostCmdOpts{
		args: extraArgs,
		env:  []string{"SLATE_AGENT=1"},
	})
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

// appendShellArgs adds passthrough arguments to a slate.yml command. The
// command is a shell string, so each argument is single-quoted to reach the
// process intact. Appended after placeholder expansion: a caller's prompt
// that happens to contain {{PROJECT}} is its own text, not a template.
func appendShellArgs(command string, args []string) string {
	var b strings.Builder
	b.WriteString(command)
	for _, a := range args {
		b.WriteString(" '")
		b.WriteString(strings.ReplaceAll(a, "'", `'\''`))
		b.WriteString("'")
	}
	return b.String()
}

// slateBinEnv points a host command's `slate` at the binary running it.
// These commands are often launched by a scheduler whose PATH is minimal, or
// which carries an older slate ahead of this one; either way `slate agent` in
// a hook has to mean this slate rather than whatever PATH happens to resolve.
func slateBinEnv() []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	return []string{
		"SLATE_BIN=" + exe,
		"PATH=" + filepath.Dir(exe) + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
}

// hostCmdOpts carries the per-call variations of a host command: arguments
// appended to the configured command, and extra environment.
type hostCmdOpts struct {
	args []string
	env  []string
}

// runHostCommand executes a slate.yml host command via sh -c in the workspace
// dir. Ordinary non-zero exits (ctrl-c, `exit 1`) aren't slate failures, but
// 126 and 127 mean the command itself couldn't run and must be surfaced.
func runHostCommand(cfg config.ProjectConfig, command, wsName, wsDir string, fresh bool, opts hostCmdOpts) error {
	project, err := workspace.ProjectName(cfg.Project)
	if err != nil {
		return err
	}
	expanded := appendShellArgs(expandCommand(command, wsName, project), opts.args)

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
	c.Env = append(c.Env, slateBinEnv()...)
	c.Env = append(c.Env, opts.env...)
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

// upAt is what new/up drop into after provisioning: the up hook if
// configured (behind the --hooks gate), then a shell (behind auto_cd/--cd).
func upAt(cfg config.ProjectConfig, wsName, wsDir string, fresh, cd, hooks bool) error {
	if hooks && cfg.Up != "" {
		if err := runHostCommand(cfg, cfg.Up, wsName, wsDir, fresh, hookOpts()); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: %v\n", err)
		}
	}
	if !cd {
		return nil
	}
	return spawnShellUnlessFinished(wsDir)
}

// hookOpts is the environment a new:/up: hook runs with. SLATE_HOOKS=1 keeps
// the chain the hook starts enabled: a hook that runs `slate agent` must not
// then hit the terminal check, since the operator already opted in here.
func hookOpts() hostCmdOpts {
	return hostCmdOpts{env: []string{"SLATE_HOOKS=1"}}
}
