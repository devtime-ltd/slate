package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/devtime-ltd/slate/internal/compose"
	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/proxy"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var rmForce bool

var rmCmd = &cobra.Command{
	Use:   "rm [name]",
	Short: "Destroy workspace (containers, volumes, worktree)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runRm,
}

func init() {
	rmCmd.Flags().BoolVarP(&rmForce, "force", "f", false, "Skip confirmation prompt")
	rmCmd.GroupID = "workspace"
	rootCmd.AddCommand(rmCmd)
}

func runRm(cmd *cobra.Command, args []string) error {
	if err := requireDocker(); err != nil {
		return err
	}
	name, err := resolveWorkspaceArg(args)
	if err != nil {
		return err
	}
	if err := workspace.ValidateName(name); err != nil {
		return err
	}

	wsDir, err := workspace.WorkspaceDir(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(wsDir); err != nil {
		return fmt.Errorf("workspace '%s' not found", name)
	}

	hostname, err := resolveHostname(name)
	if err != nil {
		return err
	}

	mainRoot, _ := workspace.MainRoot()
	cwdInside := cwdIsInside(wsDir)
	dirty, isDirty := dirtyWorktreeSummary(wsDir)

	if isDirty && rmForce {
		fmt.Fprintf(os.Stderr, "warning: %s has uncommitted changes (%s)\n", name, dirty)
	}

	if !rmForce {
		if isDirty {
			fmt.Printf("%s has uncommitted changes (%s).\n", name, dirty)
		}
		fmt.Printf("This will destroy %s (containers, volumes, worktree). Continue? [y/N] ", hostname)
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(answer)) != "y" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	killProvisioningLock(wsDir)

	env, err := compose.NewEnv(name, wsDir, hostname)
	if err != nil {
		return err
	}

	fmt.Printf("Destroying %s...\n", hostname)
	compose.Run(env, "down", "-v")

	cfg, _ := config.LoadProject(mainRoot)
	proxy.Unregister(hostname, scaffoldSubdomains(cfg))

	workspace.RemoveWorktree(wsDir)

	fmt.Printf("" + tick() + " %s removed\n", hostname)

	if cwdInside && mainRoot != "" {
		fmt.Printf("Your cwd was destroyed; dropping into a shell at %q (exit to return).\n", mainRoot)
		return spawnShellAt(mainRoot)
	}
	return nil
}

// cwdIsInside reports whether the process CWD is at or under dir.
func cwdIsInside(dir string) bool {
	cwd, err := os.Getwd()
	if err != nil || cwd == "" {
		return false
	}
	if cwd == dir {
		return true
	}
	return strings.HasPrefix(cwd, dir+string(filepath.Separator))
}

// dirtyWorktreeSummary returns a short summary like "3 modified, 1 untracked"
// and a bool for whether the worktree has any uncommitted changes. Empty
// string + false means clean (or git failed; we treat that as clean rather
// than blocking destruction).
func dirtyWorktreeSummary(wsDir string) (string, bool) {
	c := exec.Command("git", "status", "--porcelain")
	c.Dir = wsDir
	out, err := c.Output()
	if err != nil {
		return "", false
	}
	body := strings.TrimSpace(string(out))
	if body == "" {
		return "", false
	}
	var modified, untracked int
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "??") {
			untracked++
		} else {
			modified++
		}
	}
	var parts []string
	if modified > 0 {
		parts = append(parts, fmt.Sprintf("%d modified", modified))
	}
	if untracked > 0 {
		parts = append(parts, fmt.Sprintf("%d untracked", untracked))
	}
	return strings.Join(parts, ", "), true
}
