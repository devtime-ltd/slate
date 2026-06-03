package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

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
	cfg, err := config.LoadProject(mainRoot)
	if err != nil {
		return err
	}
	wsDir, err := workspace.WorkspaceDir(name)
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

// runBackgroundProvision forks the bg provisioner and then either drops into
// a shell at the workspace dir (cd=true) or prints the path and exits.
func runBackgroundProvision(name, wsDir string, opts provisionOpts, cd bool) error {
	if err := detachProvision(name, wsDir, opts); err != nil {
		return err
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

	logPath := filepath.Join(wsDir, ".slate", "provision.log")
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
	if err := c.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return nil
		}
		return err
	}
	return nil
}

