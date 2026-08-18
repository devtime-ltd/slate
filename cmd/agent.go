package cmd

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

var agentNoHold bool

var agentCmd = &cobra.Command{
	Use:   "agent [workspace]",
	Short: "Run the project's agent command in a workspace",
	Long: `Runs the "agent:" command from the main checkout's slate.yml in the
workspace directory. Configure a single command, or a [first-run, thereafter]
pair; the first-run variant is picked on the workspace's first agent entry
(tracked via .slate/agent-started), whether that entry comes through the up
hook (SLATE_FRESH=1) or directly in a bare workspace.

A command that returns before it could have hosted a session is treated as a
failed launch rather than a clean exit: slate reports it, leaves a shell in
the workspace, and doesn't record the entry. Pass --no-hold to exit instead.

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
			return holdWorkspaceOpen(wsDir, agentUnconfiguredError(mainRoot, wsDir))
		}
		return runAgent(cfg, name, wsDir, agentFresh(wsDir))
	},
}

func agentStartedMarker(wsDir string) string {
	return filepath.Join(wsDir, ".slate", "agent-started")
}

func firstRunPendingMarker(wsDir string) string {
	return filepath.Join(wsDir, ".slate", "agent-first-run-pending")
}

// agentFresh decides whether this is the workspace's first agent entry.
// The agent-started marker is the source of truth once it exists: it stops
// a bare workspace's later `slate up` (which sets SLATE_FRESH=1) from
// re-running the first-run variant over a live session. A recorded failed
// first-run launch keeps the workspace fresh for its next entry: SLATE_FRESH
// and bareness are gone by then, and without the marker the entry would fall
// through to the thereafter variant with the first-run command still owed.
// Otherwise, SLATE_FRESH=1 (up hook) or a bare workspace means first entry;
// existing pre-marker workspaces fall through to the thereafter variant.
func agentFresh(wsDir string) bool {
	if _, err := os.Stat(agentStartedMarker(wsDir)); err == nil {
		return false
	}
	if _, err := os.Stat(firstRunPendingMarker(wsDir)); err == nil {
		return true
	}
	if os.Getenv("SLATE_FRESH") == "1" {
		return true
	}
	_, err := os.Stat(unprovisionedMarker(wsDir))
	return err == nil
}

func init() {
	agentCmd.Flags().BoolVar(&agentNoHold, "no-hold", false, "exit on a failed agent launch instead of leaving a shell in the workspace")
	rootCmd.AddCommand(agentCmd)
}

// agentUnconfiguredError explains a missing `agent:` in terms of where slate
// reads it from. The trap this catches: an `agent:` that lives only in the
// worktree's slate.yml is invisible to slate, so the session never starts.
func agentUnconfiguredError(mainRoot, wsDir string) error {
	mainYml := filepath.Join(mainRoot, "slate.yml")
	if wsCfg, err := config.LoadProject(wsDir); err == nil && !wsCfg.Agent.IsZero() {
		return fmt.Errorf("no `agent:` in %s; this workspace's slate.yml sets one, but host commands only ever come from the main checkout, so land it there", mainYml)
	}
	return fmt.Errorf("no `agent:` in %s; set a command (e.g. `agent: claude`) or a [first-run, thereafter] pair", mainYml)
}

func runAgent(cfg config.ProjectConfig, wsName, wsDir string, fresh bool) error {
	command, variant := cfg.Agent.Again, "thereafter"
	if fresh {
		command, variant = cfg.Agent.First, "first-run"
	}

	run, err := runHostCommandDetail(cfg, command, wsName, wsDir, fresh)
	recordAgentRun(wsDir, variant, run)
	finalFresh := fresh
	if err == nil && run.bailed() {
		err = agentBailedError(run, variant)
		// Bailing without running means the command declined the work it was
		// given, and for the thereafter variant that means it presumed a
		// session the workspace hasn't got (`claude --continue` with nothing
		// to continue exits 0 or 1 depending on the claude build). The
		// first-run variant is what should have run. Signal deaths (-1, >128)
		// are the launch being stopped, not declined: don't start another.
		// 126/127 never reach here (runHostCommandDetail returns them as
		// errors): a command that couldn't run is a config problem to
		// surface, and retrying the other variant would mask it.
		if run.exitCode >= 0 && run.exitCode <= 128 && !fresh && cfg.Agent.First != "" && cfg.Agent.First != cfg.Agent.Again {
			fmt.Fprintf(os.Stderr, "  %s %v\n", warn(), err)
			fmt.Fprintln(os.Stderr, "  retrying with the first-run variant")
			finalFresh = true
			run, err = runHostCommandDetail(cfg, cfg.Agent.First, wsName, wsDir, true)
			recordAgentRun(wsDir, "first-run", run)
			if err == nil && run.bailed() {
				err = agentBailedError(run, "first-run")
			}
		}
	}
	if err != nil {
		// The freshness signals (SLATE_FRESH, bareness) won't survive to the
		// next invocation, so a failed first-run launch is persisted or the
		// retry would land on the thereafter variant.
		if finalFresh {
			warnOnMarkerError("the workspace will not remember it is owed a first-run entry",
				writeWorkspaceMarker(wsDir, "agent-first-run-pending", nil))
		}
		return holdWorkspaceOpen(wsDir, err)
	}
	_ = removeWorkspaceMarker(wsDir, "agent-first-run-pending")
	warnOnMarkerError("the next entry will re-run the first-run variant over this workspace",
		writeWorkspaceMarker(wsDir, "agent-started", nil))
	return nil
}

// warnOnMarkerError surfaces a failed state-bearing marker write: losing one
// silently would misroute the workspace's next variant choice.
func warnOnMarkerError(consequence string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "  %s could not record agent state (%v); %s\n", warn(), err, consequence)
	}
}

// openSlateDir pins the workspace's real .slate directory. The worktree,
// .slate included, is container-writable, so every host-side marker operation
// goes through this fd: O_NOFOLLOW refuses a link planted in the directory's
// place, and *at syscalls on the fd never re-walk the path, so a concurrent
// swap can't redirect them to files outside the workspace.
func openSlateDir(wsDir string) (*os.File, error) {
	return os.OpenFile(filepath.Join(wsDir, ".slate"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
}

func writeWorkspaceMarker(wsDir, name string, data []byte) error {
	dir, err := openSlateDir(wsDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_TRUNC|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	_, err = f.Write(data)
	return err
}

func removeWorkspaceMarker(wsDir, name string) error {
	dir, err := openSlateDir(wsDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return unix.Unlinkat(int(dir.Fd()), name, 0)
}

// recordAgentRun drops each agent run's outcome into the workspace. An agent
// exit outside the bail window returns cleanly and takes any enclosing tmux
// session with it, so this file is the only evidence of what happened.
func recordAgentRun(wsDir, variant string, run hostRun) {
	if run.command == "" {
		return
	}
	line := fmt.Sprintf("%s variant=%s exit=%d elapsed=%s command=%s\n",
		time.Now().Format(time.RFC3339), variant, run.exitCode, run.elapsed.Round(time.Millisecond), run.command)
	_ = writeWorkspaceMarker(wsDir, "agent-last-run", []byte(line))
}

func agentBailedError(run hostRun, variant string) error {
	return fmt.Errorf("the %s agent command exited after %s without starting a session (exit %d): %s",
		variant, run.elapsed.Round(time.Millisecond), run.exitCode, run.command)
}

// holdWorkspaceOpen reports a failed agent launch and, at a terminal, leaves a
// shell in the workspace instead of returning. The documented tmux recipe makes
// `slate agent` the session's only command, so returning would tear the session
// down and take the diagnostic with it.
func holdWorkspaceOpen(wsDir string, cause error) error {
	if agentNoHold || !isInteractiveTerminal() {
		return cause
	}
	fmt.Fprintf(os.Stderr, "\n%s %v\n", cross(), cause)
	fmt.Fprintln(os.Stderr, "Holding the workspace open with a shell; fix the command, then run `slate agent` again.")
	return spawnShellAt(wsDir)
}

func expandCommand(command, wsName, project string) string {
	return strings.NewReplacer(
		"{{WORKSPACE}}", wsName,
		"{{PROJECT}}", project,
		"{{HOSTNAME}}", workspace.HostnameForProject(project, wsName),
	).Replace(command)
}

// hostRun is how a slate.yml host command finished. Elapsed time is what
// separates an agent session that ran from a launch that never got off the
// ground: a command can fail on its own terms and still exit 0, which is
// otherwise indistinguishable from a clean quit.
type hostRun struct {
	command  string
	elapsed  time.Duration
	exitCode int
}

func (r hostRun) bailed() bool {
	floor := agentMinRuntime()
	return floor > 0 && r.elapsed < floor
}

const (
	defaultAgentMinRuntime = 3 * time.Second
	maxAgentMinRuntime     = time.Hour
)

// agentMinRuntime is how long an agent command has to survive before slate
// believes a session started. SLATE_AGENT_MIN_RUNTIME (seconds, 0 to disable)
// overrides it, for an `agent:` that legitimately hands off and returns.
// NaN and +Inf parse without error but convert to a 0s or 292-year duration,
// which would silently disable the check or fail every launch, so the range
// is bounded rather than just non-negative.
func agentMinRuntime() time.Duration {
	secs, err := strconv.ParseFloat(os.Getenv("SLATE_AGENT_MIN_RUNTIME"), 64)
	if err != nil || math.IsNaN(secs) || secs < 0 || secs > maxAgentMinRuntime.Seconds() {
		return defaultAgentMinRuntime
	}
	return time.Duration(secs * float64(time.Second))
}

// runHostCommand executes a slate.yml host command via sh -c in the workspace
// dir. Ordinary non-zero exits (ctrl-c, `exit 1`) aren't slate failures, but
// 126 and 127 mean the command itself couldn't run and must be surfaced.
func runHostCommand(cfg config.ProjectConfig, command, wsName, wsDir string, fresh bool) error {
	_, err := runHostCommandDetail(cfg, command, wsName, wsDir, fresh)
	return err
}

func runHostCommandDetail(cfg config.ProjectConfig, command, wsName, wsDir string, fresh bool) (hostRun, error) {
	project, err := workspace.ProjectName(cfg.Project)
	if err != nil {
		return hostRun{}, err
	}
	expanded := expandCommand(command, wsName, project)
	run := hostRun{command: expanded}
	if note := hookNeedsAgentNote(cfg, expanded); note != "" {
		fmt.Fprintln(os.Stderr, note)
	}

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

	started := time.Now()
	err = c.Run()
	run.elapsed = time.Since(started)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			run.exitCode = ee.ExitCode()
			if run.exitCode == 126 || run.exitCode == 127 {
				return run, fmt.Errorf("command failed (exit %d, not found/executable?): %s", run.exitCode, expanded)
			}
			return run, nil
		}
		// a start failure has no exit code; without this the breadcrumb
		// would claim exit=0
		run.exitCode = -1
		return run, err
	}
	return run, nil
}

// hookNeedsAgentNote catches a `new:`/`up:` hook reaching for an `agent:` that
// isn't configured: the hook's own `slate agent` would fail inside a process
// whose output the hook may never show.
func hookNeedsAgentNote(cfg config.ProjectConfig, expanded string) string {
	if !cfg.Agent.IsZero() || !strings.Contains(expanded, "slate agent") {
		return ""
	}
	return fmt.Sprintf("  %s this hook runs `slate agent`, but the main checkout's slate.yml has no `agent:`; no session will start", warn())
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
