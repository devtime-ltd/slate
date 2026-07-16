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

	hostname, err := resolveHostname(name)
	if err != nil {
		return err
	}

	if _, err := os.Stat(wsDir); err != nil {
		return rmOrphaned(name, wsDir, hostname)
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
	compose.Run(env, "down", "-v", "--remove-orphans")

	cfg, _ := config.LoadProject(mainRoot)
	proxy.Unregister(hostname, scaffoldSubdomains(cfg))

	workspace.RemoveWorktree(wsDir)

	fmt.Printf(""+tick()+" %s removed\n", hostname)

	if cwdInside && mainRoot != "" {
		if insideSlateShell() {
			fmt.Println("Your cwd was destroyed; exiting the slate shell.")
			popSlateShell()
			return nil
		}
		fmt.Printf("Your cwd was destroyed; dropping into a shell at %q (exit to return).\n", mainRoot)
		return spawnShellAt(mainRoot)
	}
	return nil
}

// rmOrphaned cleans up after a workspace whose directory was deleted manually
// (rm -rf instead of `slate rm`): labeled docker containers/volumes, the proxy
// routes, and the stale git worktree registration.
func rmOrphaned(name, wsDir, hostname string) error {
	project := compose.ProjectName(hostname)
	hasDocker := dockerProjectHasResources(project)
	registered := workspace.WorktreeRegistered(wsDir)
	if !hasDocker && !registered {
		return fmt.Errorf("workspace '%s' not found", name)
	}

	var leftovers []string
	if hasDocker {
		leftovers = append(leftovers, "containers/volumes")
	}
	if registered {
		leftovers = append(leftovers, "a stale worktree registration")
	}

	if !rmForce {
		fmt.Printf("%s's directory is already gone but it left behind %s. Clean up? [y/N] ", name, strings.Join(leftovers, " and "))
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(answer)) != "y" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	fmt.Printf("Cleaning up %s...\n", hostname)
	if hasDocker {
		compose.DownProject(project, "-v", "--remove-orphans")
	}

	mainRoot, _ := workspace.MainRoot()
	cfg, _ := config.LoadProject(mainRoot)
	proxy.Unregister(hostname, scaffoldSubdomains(cfg))

	if registered {
		if err := workspace.PruneWorktrees(); err != nil {
			return err
		}
	}

	fmt.Printf(""+tick()+" %s removed\n", hostname)
	return nil
}

func dockerProjectHasResources(project string) bool {
	filter := "label=com.docker.compose.project=" + project
	for _, args := range [][]string{
		{"ps", "-aq", "--filter", filter},
		{"volume", "ls", "-q", "--filter", filter},
	} {
		out, err := exec.Command("docker", args...).Output()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			return true
		}
	}
	return false
}

// cwdIsInside reports whether the process CWD is at or under dir, resolving
// symlinks on both sides (cwd follows $PWD, which may be a symlinked path).
func cwdIsInside(dir string) bool {
	cwd, err := os.Getwd()
	if err != nil || cwd == "" {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
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
		if len(line) < 3 {
			continue
		}
		// .env.container is generated by slate and regenerated on every up/new.
		// Skip it only while untracked (the normal case) so it doesn't trip the
		// warning on every pristine workspace; if it is somehow tracked and
		// modified, fall through and count it so real changes still warn.
		if strings.HasPrefix(line, "??") && strings.TrimSpace(line[2:]) == ".env.container" {
			continue
		}
		if strings.HasPrefix(line, "??") {
			untracked++
		} else {
			modified++
		}
	}
	if modified == 0 && untracked == 0 {
		return "", false
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
