package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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

// destroyWorkspace tears down a workspace's containers, volumes, proxy routes,
// worktree and (unless kept) its branch. escapeCwd controls whether a caller
// whose cwd was inside may be dropped into a shell at the main root - false for
// the agent exit hook, whose cwd is pinned to the worktree rather than the
// user's shell. verified, when set, re-checks the workspace hasn't changed
// since it was proven landed, aborting rather than destroying stale evidence.
func destroyWorkspace(name, wsDir, hostname string, keepBranch bool, provenLanded string, escapeCwd bool, verified *landedEvidence) error {
	mainRoot, _ := workspace.MainRoot()
	cwdInside := cwdIsInside(wsDir)

	// A verified `slate done` teardown re-verifies the workspace is still
	// exactly as checkLanded left it BEFORE anything irreversible: `compose
	// down -v` destroys volumes, so a change slipping in after the landed-check
	// must abort here, with nothing yet lost. It also never kills a provisioner
	// (only the force `rm` path does).
	if verified != nil {
		if problem := reverifyUnchanged(verified, wsDir); problem != "" {
			return fmt.Errorf("aborting removal of %s: %s after verification; nothing was destroyed - re-run `slate done` once it has landed, or `slate rm` to force", name, problem)
		}
	} else {
		killProvisioningLock(wsDir)
	}

	// Only a bare workspace (never provisioned) may skip container teardown:
	// silently proceeding for a provisioned one would orphan its containers.
	bare := false
	if _, err := os.Stat(unprovisionedMarker(wsDir)); err == nil {
		bare = true
	}
	var downErr error
	if requireDocker() == nil {
		if env, err := compose.NewEnv(name, wsDir, hostname); err == nil {
			fmt.Printf("Destroying %s...\n", hostname)
			downErr = compose.Run(env, "down", "-v", "--remove-orphans")
		} else if !bare {
			fmt.Printf("Destroying %s...\n", hostname)
			downErr = compose.DownProject(compose.ProjectName(hostname), "-v", "--remove-orphans")
		}
	} else if !bare {
		return fmt.Errorf("docker not found in PATH; it is needed to destroy %s's containers (only bare workspaces can be removed without it)", hostname)
	}

	if downErr != nil && verified != nil {
		// Abort before unregistering the proxy: leaving routes in place while
		// the containers may still be running is better than stripping their
		// HTTPS endpoint and orphaning them behind a "done".
		return fmt.Errorf("aborting removal of %s: tearing its containers down failed, so the worktree is kept to avoid orphaning them: %w", name, downErr)
	}

	proxy.UnregisterAll(hostname)

	if downErr != nil {
		fmt.Fprintf(os.Stderr, "warning: tearing down %s reported an error (removing the worktree anyway): %v\n", hostname, downErr)
	}

	branch := workspace.WorktreeBranch(wsDir)
	removeErr := workspace.RemoveWorktree(wsDir)
	if removeErr != nil {
		if verified != nil {
			return fmt.Errorf("could not remove %s's worktree: %w", name, removeErr)
		}
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
			fmt.Printf("If your shell was inside the workspace, run `cd %s`.\n", mainRoot)
			return nil
		}
		if insideSlateShell() {
			fmt.Println("Your cwd was destroyed; exiting the slate shell.")
			popSlateShell()
			return nil
		}
		if !isInteractiveTerminal() {
			fmt.Printf("Your cwd was destroyed; run `cd %s`.\n", mainRoot)
			return nil
		}
		fmt.Printf("Your cwd was destroyed; dropping into a shell at %q (exit to return).\n", mainRoot)
		return spawnShellAt(mainRoot)
	}
	return nil
}

// reverifyUnchanged reports why a verified workspace is no longer safe to
// destroy - a provisioner appeared, the worktree went dirty, or its branch/tip
// moved - or "" when it still matches the landed evidence. Anchored to the main
// checkout's registration so a swapped `.git` can't hide a change.
func reverifyUnchanged(ev *landedEvidence, wsDir string) string {
	if _, provisioning := readProvisioningLock(wsDir); provisioning {
		return "provisioning started again"
	}
	summary, dirty, serr := worktreeStatusFor(ev.gitDir, wsDir)
	branchNow, berr := gitFor(ev.gitDir, wsDir, "rev-parse", "--abbrev-ref", "HEAD")
	tipNow, terr := gitFor(ev.gitDir, wsDir, "rev-parse", "HEAD")
	switch {
	case serr != nil || berr != nil || terr != nil:
		return "could not re-verify its state"
	case dirty:
		return "uncommitted changes appeared (" + summary + ")"
	case branchNow != ev.branch || tipNow != ev.tip:
		return "the checked-out branch or tip changed from what was verified"
	}
	return ""
}

// worktreeStatus summarises a worktree's uncommitted changes (informational,
// e.g. the rm confirmation), reading via the worktree's own git dir.
func worktreeStatus(wsDir string) (string, bool, error) {
	return worktreeStatusFor("", wsDir)
}

// worktreeStatusFor is worktreeStatus anchored to the main checkout's
// registration when gitDir is set (so a swapped `.git` can't hide changes from
// a safety check), falling back to the work tree's own git otherwise.
func worktreeStatusFor(gitDir, workTree string) (string, bool, error) {
	raw, err := worktreeStatusRaw(gitDir, workTree)
	if err != nil {
		return "", false, err
	}
	return parseStatus(raw)
}

// worktreeStatusRaw pins --untracked-files=normal: status.showUntrackedFiles=no
// would hide untracked work and =all would list .slate/ file by file. Dirty
// submodules stay visible, and a directory git warned it could not open
// (left out of the listing, exit 0) is listed as untracked: this status
// guards slate done's teardown.
func worktreeStatusRaw(gitDir, workTree string) (string, error) {
	out, warnings, err := gitForRaw(gitDir, workTree, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return "", fmt.Errorf("git status failed in %s: %v", workTree, err)
	}
	unopened, err := unopenedDirectories(warnings)
	if err != nil {
		return "", fmt.Errorf("git status in %s: %w", workTree, err)
	}
	var listing strings.Builder
	listing.Write(out)
	for _, dir := range unopened {
		fmt.Fprintf(&listing, "?? %s/\n", dir)
	}
	return listing.String(), nil
}

// slateGenerated names the entries slate itself writes: .env.container and
// the .slate/ directory's contents. A plain file or link named .slate is not
// one of them.
func slateGenerated(path string) bool {
	return path == ".env.container" || path == ".slate/" || strings.HasPrefix(path, ".slate/")
}

// statusLines keeps the porcelain lines that are work, dropping the
// slate-generated .env.container and .slate/ while they are merely untracked.
// The path starts after exactly one separator space; its own whitespace is
// part of the name.
func statusLines(porcelain string) []string {
	var lines []string
	for _, line := range strings.Split(porcelain, "\n") {
		if len(line) < 4 || (strings.HasPrefix(line, "?? ") && slateGenerated(line[3:])) {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func parseStatus(porcelain string) (string, bool, error) {
	var modified, untracked int
	for _, line := range statusLines(porcelain) {
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
	if !rmKeepBranch {
		cleanupBranch(mainRoot, branch, "")
	}
	return nil
}

// cleanupBranch deletes the workspace's branch when its commits are provably
// recoverable (pushed or merged), otherwise says why it was kept.
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

// dirtyWorktreeSummary is worktreeStatus for callers that only warn (the rm
// confirmation prompt): a status that cannot be taken warns with its reason.
func dirtyWorktreeSummary(wsDir string) (string, bool) {
	summary, dirty, err := worktreeStatus(wsDir)
	if err != nil {
		return "could not verify: " + err.Error(), true
	}
	return summary, dirty
}
