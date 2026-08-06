package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/devtime-ltd/slate/internal/compose"
	"github.com/devtime-ltd/slate/internal/proxy"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	rmForce      bool
	rmKeepBranch bool
)

var rmCmd = &cobra.Command{
	Use:   "rm [name]",
	Short: "Destroy workspace (containers, volumes, worktree)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runRm,
}

func init() {
	rmCmd.Flags().BoolVarP(&rmForce, "force", "f", false, "Skip confirmation prompt")
	rmCmd.Flags().BoolVar(&rmKeepBranch, "keep-branch", false, "Keep the workspace branch even if pushed/merged")
	rmCmd.GroupID = "workspace"
	rootCmd.AddCommand(rmCmd)
}

// runRm has no hard docker requirement: bare workspaces exist without it,
// and rm must still remove the worktree. Docker teardown is best-effort.
func runRm(cmd *cobra.Command, args []string) error {
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

	return destroyWorkspace(name, wsDir, hostname, rmKeepBranch, "", true, nil)
}

// destroyWorkspace tears down a workspace's containers, volumes, proxy
// routes, worktree and (when safe) branch. Callers own any confirmation;
// this only destroys. Shared by rm, done, and the agent exit hook.
// A non-empty provenLanded is evidence (from checkLanded) that the branch's
// work merged even where BranchSafety can't see it - rebase- and
// squash-merges rewrite SHAs - and lets the branch be deleted anyway.
// A non-nil verified re-verifies the worktree immediately before removal:
// the evidence-gated callers checked it before the (slow) container and
// proxy teardown, and anything written meanwhile - or a switch to a
// different branch/tip than the one the evidence covered - must not be
// force-removed on stale proof.
func destroyWorkspace(name, wsDir, hostname string, keepBranch bool, provenLanded string, escapeCwd bool, verified *landedEvidence) error {
	mainRoot, _ := workspace.MainRoot()
	cwdInside := cwdIsInside(wsDir)

	killProvisioningLock(wsDir)

	// Only a bare workspace (never provisioned) may skip container teardown:
	// silently proceeding for a provisioned one would orphan its containers
	// and volumes behind a successful-looking rm.
	bare := false
	if _, err := os.Stat(unprovisionedMarker(wsDir)); err == nil {
		bare = true
	}
	if requireDocker() == nil {
		if env, err := compose.NewEnv(name, wsDir, hostname); err == nil {
			fmt.Printf("Destroying %s...\n", hostname)
			compose.Run(env, "down", "-v", "--remove-orphans")
		} else if !bare {
			// No compose env (entrypoint missing) but the workspace was
			// provisioned: tear down by label instead of orphaning.
			fmt.Printf("Destroying %s...\n", hostname)
			compose.DownProject(compose.ProjectName(hostname), "-v", "--remove-orphans")
		}
	} else if !bare {
		return fmt.Errorf("docker not found in PATH; it is needed to destroy %s's containers (only bare workspaces can be removed without it)", hostname)
	}

	proxy.UnregisterAll(hostname)

	// Last possible moment before the forced removal: container and proxy
	// teardown above took real time, and anything written meanwhile - or a
	// branch/tip swap that would invalidate the evidence - must abort.
	if verified != nil {
		summary, dirty, serr := worktreeStatus(wsDir)
		branchNow, berr := gitIn(wsDir, "rev-parse", "--abbrev-ref", "HEAD")
		tipNow, terr := gitIn(wsDir, "rev-parse", "HEAD")
		var problem string
		switch {
		case serr != nil || berr != nil || terr != nil:
			problem = "could not re-verify its state"
		case dirty:
			problem = "uncommitted changes appeared (" + summary + ")"
		case branchNow != verified.branch || tipNow != verified.tip:
			problem = "the checked-out branch or tip changed from what was verified"
		}
		if problem != "" {
			return fmt.Errorf("aborting removal of %s: %s after verification; containers are stopped - `slate up` restores them, `slate rm` removes anyway", name, problem)
		}
	}

	branch := workspace.WorktreeBranch(wsDir)
	removeErr := workspace.RemoveWorktree(wsDir)
	if removeErr != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", removeErr)
	}

	fmt.Printf(""+tick()+" %s removed\n", hostname)
	if removeErr == nil {
		_ = os.Remove(stagedTeardownMarker(wsDir))
		_ = os.Remove(teardownDeclinedMarker(wsDir))
		if !keepBranch {
			cleanupBranch(mainRoot, branch, provenLanded)
		}
	}

	if cwdInside && mainRoot != "" {
		if !escapeCwd {
			// The caller's cwd is not the user's shell (the agent exit hook
			// runs with cwd pinned to the worktree); escapes here would act
			// on the wrong process. A hint covers the case where the user's
			// shell really was inside.
			fmt.Printf("If your shell was inside the workspace, run `cd %s`.\n", mainRoot)
			return nil
		}
		if insideSlateShell() {
			fmt.Println("Your cwd was destroyed; exiting the slate shell.")
			popSlateShell()
			return nil
		}
		if !term.IsTerminal(os.Stdin.Fd()) {
			// Non-interactive caller (an agent session, a script): a spawned
			// shell would just read EOF. Point at the escape route instead.
			fmt.Printf("Your cwd was destroyed; run `cd %s`.\n", mainRoot)
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
	proxy.UnregisterAll(hostname)

	// The stale registration still knows its branch; read it before pruning.
	branch := workspace.WorktreeBranch(wsDir)
	if branch == "" {
		branch = "slate/" + name
	}

	if registered {
		if err := workspace.PruneWorktrees(); err != nil {
			return err
		}
	}

	fmt.Printf(""+tick()+" %s removed\n", hostname)
	_ = os.Remove(stagedTeardownMarker(wsDir))
	_ = os.Remove(teardownDeclinedMarker(wsDir))
	if !rmKeepBranch {
		cleanupBranch(mainRoot, branch, "")
	}
	return nil
}

// cleanupBranch deletes the workspace's branch when its commits are provably
// recoverable (pushed or merged), otherwise says why it was kept. A non-empty
// provenLanded overrides an unsafe verdict: the caller verified the merge in
// a way BranchSafety can't (a merged PR after a rebase/squash rewrite).
func cleanupBranch(mainRoot, branch, provenLanded string) {
	if branch == "" || mainRoot == "" {
		return
	}
	if branch == workspace.DefaultBranch(mainRoot) {
		return // never the default branch, whatever the evidence claims
	}
	safe, reason := workspace.BranchSafety(mainRoot, branch)
	if reason == "no such branch" {
		return
	}
	if workspace.CheckedOutBranches()[branch] {
		return // in use by another worktree; not this workspace's to delete
	}
	if !safe && provenLanded != "" {
		safe, reason = true, provenLanded
	}
	if !safe {
		fmt.Printf("  kept branch %s (%s); delete with `git branch -D %s`\n", branch, reason, branch)
		return
	}
	if err := workspace.DeleteBranch(mainRoot, branch); err != nil {
		fmt.Printf("  warning: could not delete branch %s: %v\n", branch, err)
		return
	}
	fmt.Printf("  deleted branch %s (%s)\n", branch, reason)
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
// and a bool for whether the worktree has any uncommitted changes. A git
// failure reads as clean: rm's flow warns and confirms with a human, so
// failing open is acceptable there. checkLanded must NOT use this - it
// feeds promptless destruction and uses worktreeStatus instead.
func dirtyWorktreeSummary(wsDir string) (string, bool) {
	summary, dirty, _ := worktreeStatus(wsDir)
	return summary, dirty
}

// worktreeStatus is the error-honest variant: err!=nil means git could not
// answer, and the worktree must not be presumed clean.
func worktreeStatus(wsDir string) (string, bool, error) {
	c := exec.Command("git", "status", "--porcelain")
	c.Dir = wsDir
	out, err := c.Output()
	if err != nil {
		return "", false, fmt.Errorf("git status failed in %s: %v", wsDir, err)
	}
	body := strings.TrimSpace(string(out))
	if body == "" {
		return "", false, nil
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
		return "", false, nil
	}
	var parts []string
	if modified > 0 {
		parts = append(parts, fmt.Sprintf("%d modified", modified))
	}
	if untracked > 0 {
		parts = append(parts, fmt.Sprintf("%d untracked", untracked))
	}
	return strings.Join(parts, ", "), true, nil
}
