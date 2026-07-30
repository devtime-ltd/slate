package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/devtime-ltd/slate/internal/compose"
	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	provisionFresh bool
	provisionBuild bool
	provisionWipe  bool
)

// provisionCmd is the slow-path worker invoked in the background by --bg.
// It assumes the worktree and .slate/ files already exist. Runs compose up,
// lifecycle, proxy registration.
var provisionCmd = &cobra.Command{
	Use:    "_provision <name>",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE:   runProvision,
}

func init() {
	provisionCmd.Flags().BoolVar(&provisionFresh, "fresh", false, "Treat as fresh provision (new workspace lifecycle)")
	provisionCmd.Flags().BoolVar(&provisionBuild, "build", false, "Force image rebuild")
	provisionCmd.Flags().BoolVar(&provisionWipe, "wipe", false, "Run compose down -v first")
	rootCmd.AddCommand(provisionCmd)
}

func runProvision(cmd *cobra.Command, args []string) error {
	name := args[0]

	mainRoot, err := workspace.MainRoot()
	if err != nil {
		return err
	}
	wsDir, err := workspace.WorkspaceDir(name)
	if err != nil {
		return err
	}

	// Handshake: the parent publishes our pid to the lock right after
	// forking us. Wait until it actually holds our pid (a stale lock from a
	// crashed run doesn't count) so that publication can't land after our
	// cleanup and resurrect a finished provision's lock; proceed after 2s
	// regardless (the parent may have died).
	for start := time.Now(); time.Since(start) < 2*time.Second; {
		if pid, _ := readProvisioningLock(wsDir); pid == os.Getpid() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cfg, err := config.LoadProjectForWorkspace(mainRoot, wsDir)
	if err != nil {
		return err
	}
	hostname, err := resolveHostname(name)
	if err != nil {
		return err
	}

	proxyConfig, err := loadProxyConfig(false)
	if err != nil {
		return err
	}

	env, err := compose.NewEnv(name, wsDir, hostname)
	if err != nil {
		return err
	}

	return runWorkspaceLifecycle(env, name, wsDir, hostname, cfg, proxyConfig, provisionOpts{
		fresh: provisionFresh,
		build: provisionBuild,
		wipe:  provisionWipe,
	})
}

// runBackgroundProvision forks the bg provisioner, then runs the `new:` hook
// (when configured and cd-gated), drops into a shell at the workspace dir
// (cd=true), or prints the path and exits. Never the up hook: the containers
// are still provisioning; the new hook exists precisely to run before them.
func runBackgroundProvision(cfg config.ProjectConfig, name, wsDir string, opts provisionOpts, cd bool, newHook string) error {
	if err := detachProvision(name, wsDir, opts); err != nil {
		return err
	}
	if cd && newHook != "" {
		if err := runHostCommand(cfg, newHook, name, wsDir, opts.fresh); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: %v\n", err)
		}
		return spawnShellAt(wsDir)
	}
	if !cfg.Agent.IsZero() {
		fmt.Println("Run `slate agent` once provisioning completes.")
	}
	if cd {
		return spawnShellAt(wsDir)
	}
	fmt.Printf("Workspace dir: %s\n", wsDir)
	return nil
}

// detachProvision forks `slate _provision <name>` in the background, with
// output logged to .slate/workspaces/<name>/.slate/provision.log.
func detachProvision(name, wsDir string, opts provisionOpts) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	logPath := provisionLogPath(wsDir)
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("creating log file: %w", err)
	}

	cmdArgs := []string{}
	if projectOverride != "" {
		cmdArgs = append(cmdArgs, "--project", projectOverride)
	}
	cmdArgs = append(cmdArgs, "_provision", name)
	if opts.fresh {
		cmdArgs = append(cmdArgs, "--fresh")
	}
	if opts.build {
		cmdArgs = append(cmdArgs, "--build")
	}
	if opts.wipe {
		cmdArgs = append(cmdArgs, "--wipe")
	}

	cmd := exec.Command(exe, cmdArgs...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	// Setsid creates a new session and detaches from the controlling terminal,
	// so the bg provisioner survives the parent shell closing.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("starting background process: %w", err)
	}

	// The worker writes its own lock, but only once it has booted; write it
	// here too so an agent or `slate wait` started immediately after doesn't
	// read the gap as "workspace ready". Without the lock those callers race
	// the worker, so a failed write stops the provision rather than exposing
	// that state.
	lockPath := filepath.Join(wsDir, ".slate", "provisioning")
	if err := writeFileAtomic(lockPath, []byte(fmt.Sprintf("%d\n", cmd.Process.Pid))); err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		return fmt.Errorf("could not write the provisioning lock: %w\n\nProvisioning aborted; retry with `slate up %s`", err, name)
	}

	go func() {
		cmd.Wait()
		logFile.Close()
	}()

	fmt.Printf("Provisioning in background (log: %s).\n", logPath)
	fmt.Println("Run `tail -f " + logPath + "` to follow.")
	return nil
}

// spawnShellAt runs $SHELL with cwd set to dir. Non-zero shell exits (e.g.
// `exit 1`) are swallowed so they don't look like a slate failure.
func spawnShellAt(dir string) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	c := hostCommand(shell)
	c.Dir = dir
	c.Stdin = os.Stdin
	c.Env = append(os.Environ(), "SLATE_SHELL=1")
	if err := c.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return nil
		}
		return err
	}
	return nil
}

func insideSlateShell() bool {
	return os.Getenv("SLATE_SHELL") != ""
}

// popSlateShell exits the enclosing slate-spawned shell via SIGHUP; we
// ignore HUP first because the dying shell HUPs its jobs (us).
func popSlateShell() {
	signal.Ignore(syscall.SIGHUP)
	_ = syscall.Kill(os.Getppid(), syscall.SIGHUP)
}
