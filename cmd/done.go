package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var doneCmd = &cobra.Command{
	Use:   "done [name]",
	Short: "Finish a workspace: verify the work landed, then tear it down",
	Long: `Done is the safe way to wrap up a workspace whose work has shipped. It
verifies the work landed before destroying anything:

  - the worktree is clean (no uncommitted changes)
  - the branch is merged into the default branch, or its exact tip was
    merged via a pull request targeting it (detected with gh, so rebase-
    and squash-merges count)

When the evidence is conclusive the workspace is torn down without a
prompt; otherwise done refuses and lists the reasons ('slate rm' remains
the force path).

Run for the session's own workspace inside a live agent session
(SLATE_AGENT=1), the teardown is staged instead of executed - destroying
the worktree under a running session would break it - and runs when the
session exits.`,
	GroupID: "workspace",
	Args:    cobra.MaximumNArgs(1),
	RunE:    runDone,
}

func init() {
	rootCmd.AddCommand(doneCmd)
}

// landedEvidence is the result of asking "has this workspace's work
// provably landed?". Exactly one of evidence (yes, with proof) or
// reasons (no, and why) is populated.
type landedEvidence struct {
	gitDir string // resolved from main's read-only registration, not the worktree's .git
	ok     bool
	// hasWork distinguishes "merged work" from "no commits beyond the
	// default branch". Both are safe to tear down, but only the former
	// should trigger an unsolicited offer on session exit.
	hasWork  bool
	evidence string
	reasons  []string
	// prProven marks evidence backed by a merged PR into the default
	// branch. Only this grade of evidence may override BranchSafety's
	// verdict during branch cleanup; ancestry-only evidence never does.
	prProven bool
	branch   string
	tip      string
}

func gitIn(dir string, args ...string) (string, error) {
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.Output()
	return strings.TrimSpace(string(out)), err
}

// registeredGitDir resolves a worktree's git directory from the main checkout's
// read-only .git/worktrees metadata, not the worktree's own `.git` pointer
// (which is container-writable and could be swapped for a fake repo). ok is
// false when wsDir is not a registered worktree of mainRoot.
func registeredGitDir(mainRoot, wsDir string) (gitDir string, ok bool) {
	worktreesDir, err := gitIn(mainRoot, "rev-parse", "--git-path", "worktrees")
	if err != nil {
		return "", false
	}
	if !filepath.IsAbs(worktreesDir) {
		worktreesDir = filepath.Join(mainRoot, worktreesDir)
	}
	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		return "", false
	}
	wantGit := resolvePath(filepath.Join(wsDir, ".git"))
	for _, e := range entries {
		dir := filepath.Join(worktreesDir, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "gitdir"))
		if err != nil {
			continue
		}
		if resolvePath(strings.TrimSpace(string(raw))) == wantGit {
			return dir, true
		}
	}
	return "", false
}

// resolvePath canonicalises a path for comparison, resolving symlinks where it
// can (temp dirs on macOS live behind /var -> /private/var) and falling back to
// a lexical clean when the target doesn't exist.
func resolvePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if r, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(r, filepath.Base(p))
	}
	return filepath.Clean(p)
}

// gitFor runs git for a work tree. When gitDir is set (a worktree registered
// under the main checkout's read-only .git), it anchors to that dir so a
// swapped `.git` pointer inside the work tree can't redirect the command;
// otherwise (an unregistered dir - not a slate workspace) it falls back to the
// work tree's own git.
func gitFor(gitDir, workTree string, args ...string) (string, error) {
	if gitDir != "" {
		args = append([]string{"--git-dir=" + gitDir, "--work-tree=" + workTree}, args...)
	}
	c := exec.Command("git", args...)
	if gitDir == "" {
		c.Dir = workTree
	}
	out, err := c.Output()
	return strings.TrimSpace(string(out)), err
}

func isAncestorIn(dir, commit, ref string) bool {
	c := exec.Command("git", "merge-base", "--is-ancestor", commit, ref)
	c.Dir = dir
	return c.Run() == nil
}

func isAncestorFor(gitDir, workTree, commit, ref string) bool {
	args := []string{"merge-base", "--is-ancestor", commit, ref}
	if gitDir != "" {
		args = append([]string{"--git-dir=" + gitDir, "--work-tree=" + workTree}, args...)
	}
	c := exec.Command("git", args...)
	if gitDir == "" {
		c.Dir = workTree
	}
	return c.Run() == nil
}

// prForBranch fetches the PR that best answers "did this branch's work land
// in def?" via a targeted gh query, so repo size and PR age don't matter
// (the bulk prsByBranch listing is creation-ordered and capped). A branch
// can carry several PRs (reopened, retargeted); pick by evidence value: a
// merged PR into def whose recorded head matches tip beats any merged PR
// into def, which beats an open PR (kept only for the diagnostic).
// found=false with err=nil means gh worked and no such PR exists; err!=nil
// means gh couldn't answer - callers must not treat that as "no PR".
type landedPR struct {
	Number      int    `json:"number"`
	State       string `json:"state"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
	BaseRefName string `json:"baseRefName"`
}

func prForBranch(dir, branch, def, tip string) (landedPR, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "list", "--state", "all", "--head", branch,
		"--json", "number,headRefName,headRefOid,baseRefName,state", "--limit", "20")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return landedPR{}, false, err
	}
	var prs []landedPR
	if err := json.Unmarshal(out, &prs); err != nil {
		return landedPR{}, false, err
	}
	rank := func(pr landedPR) int {
		switch {
		case pr.State == "MERGED" && pr.BaseRefName == def && pr.HeadRefOid == tip:
			return 0
		case pr.State == "MERGED" && pr.BaseRefName == def:
			return 1
		case pr.State == "OPEN":
			return 2
		default:
			return 3
		}
	}
	var best landedPR
	bestRank, found := 99, false
	for _, pr := range prs {
		if pr.State == "CLOSED" {
			continue
		}
		if r := rank(pr); r < bestRank {
			best, bestRank, found = pr, r, true
		}
	}
	return best, found, nil
}

// checkLanded gathers the evidence. Cheap local checks run first; gh is
// only consulted when the worktree is clean but ancestry can't prove the
// merge (the rebase/squash-merge case). Every uncertainty fails closed:
// this feeds promptless destruction.
func checkLanded(mainRoot, wsDir string) landedEvidence {
	var ev landedEvidence

	if pid, alive := readProvisioningLock(wsDir); alive {
		ev.reasons = append(ev.reasons, fmt.Sprintf("provisioning is in flight (pid %d); wait for it or abort with `slate rm`", pid))
		return ev
	}

	// Anchor every worktree git command to the main checkout's read-only
	// registration when this is a linked worktree, so a `.git` pointer a
	// container swapped for a fake repo can't forge "landed" evidence. An
	// unregistered dir (not a slate workspace) falls back to its own git.
	gitDir, _ := registeredGitDir(mainRoot, wsDir)
	ev.gitDir = gitDir

	summary, dirty, derr := worktreeStatusFor(gitDir, wsDir)
	if derr != nil {
		ev.reasons = append(ev.reasons, "could not verify the worktree is clean: "+derr.Error())
		return ev
	}
	if dirty {
		ev.reasons = append(ev.reasons, "uncommitted changes ("+summary+")")
	}

	branch, err := gitFor(gitDir, wsDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || branch == "" || branch == "HEAD" {
		ev.reasons = append(ev.reasons, "detached HEAD; can't tell which branch this work belongs to")
		return ev
	}
	tip, err := gitFor(gitDir, wsDir, "rev-parse", "HEAD")
	if err != nil {
		ev.reasons = append(ev.reasons, "could not read HEAD")
		return ev
	}
	ev.branch, ev.tip = branch, tip

	def := workspace.DefaultBranch(mainRoot)
	if def == "" {
		ev.reasons = append(ev.reasons, "could not determine the default branch")
		return ev
	}
	if branch == def {
		ev.reasons = append(ev.reasons, "this worktree is on the default branch ("+def+"); nothing for done to judge")
		return ev
	}

	// Ancestry against the local default branch and its remote: covers
	// fast-forward and true merges, whichever side is more up to date.
	merged, mergedInto := false, ""
	for _, ref := range []string{"refs/remotes/origin/" + def, "refs/heads/" + def} {
		if _, err := gitFor(gitDir, wsDir, "rev-parse", "--verify", "--quiet", ref); err != nil {
			continue
		}
		if isAncestorFor(gitDir, wsDir, tip, ref) {
			merged, mergedInto = true, strings.TrimPrefix(strings.TrimPrefix(ref, "refs/remotes/"), "refs/heads/")
			break
		}
	}

	if merged {
		if len(ev.reasons) > 0 {
			return ev // safe ancestry, but dirty: the reasons already say so
		}
		ev.ok = true
		defTip, _ := gitFor(gitDir, wsDir, "rev-parse", mergedInto)
		switch {
		case tip == defTip:
			// Sitting exactly on the default branch's tip: a fresh (or
			// no-work) workspace. Safe, but nothing landed here.
			ev.evidence = "no commits beyond " + mergedInto + " · worktree clean"
		default:
			// Strictly behind the tip: either merged work or a stale
			// no-work workspace. Ancestry can't tell them apart - a PR
			// merged into the default branch can, but only one whose
			// recorded head IS this tip (a reused branch name can carry
			// an older incarnation's merged PR).
			if pr, found, err := prForBranch(mainRoot, branch, def, tip); err == nil && found && pr.State == "MERGED" && pr.BaseRefName == def && pr.HeadRefOid == tip {
				ev.hasWork, ev.prProven = true, true
				ev.evidence = fmt.Sprintf("PR #%d merged into %s · worktree clean", pr.Number, def)
			} else {
				ev.evidence = "already contained in " + mergedInto + " (by ancestry) · worktree clean"
			}
		}
		return ev
	}

	// Already refusing (dirty worktree): the gh consultation below can only
	// add noise to a decided answer, so keep the cheap-checks-first promise.
	if len(ev.reasons) > 0 {
		return ev
	}

	// Ancestry can't prove it (rebase- and squash-merges rewrite the SHAs);
	// ask gh whether this exact tip merged via a PR into the default branch.
	pr, found, err := prForBranch(mainRoot, branch, def, tip)
	switch {
	case err != nil:
		ev.reasons = append(ev.reasons, "not merged into "+def+" by ancestry, and gh could not check for a merged PR (offline, or gh missing)")
	case !found:
		ev.reasons = append(ev.reasons, "not merged into "+def+" and no PR found for "+branch)
	case pr.State != "MERGED":
		ev.reasons = append(ev.reasons, fmt.Sprintf("PR #%d is still open", pr.Number))
	case pr.BaseRefName != def:
		ev.reasons = append(ev.reasons, fmt.Sprintf("PR #%d merged into %s, not %s; the work hasn't reached the default branch", pr.Number, pr.BaseRefName, def))
	case pr.HeadRefOid != tip:
		ev.reasons = append(ev.reasons, fmt.Sprintf("PR #%d merged, but the local tip has moved beyond what merged", pr.Number))
	case len(ev.reasons) == 0:
		ev.ok, ev.hasWork, ev.prProven = true, true, true
		ev.evidence = fmt.Sprintf("PR #%d merged (local tip matches) · worktree clean", pr.Number)
	}
	return ev
}

// branchOverride is what destroyWorkspace may use to overrule BranchSafety:
// only PR-proven evidence qualifies, never bare ancestry.
func (ev landedEvidence) branchOverride() string {
	if ev.prProven {
		return ev.evidence
	}
	return ""
}

// The teardown markers are control files that authorise or suppress
// destruction, so they live BESIDE the worktree in the host-only
// workspaces root, never inside it: the worktree is container-writable,
// and container code must not be able to forge them or plant symlinks
// where slate will write.
func stagedTeardownMarker(wsDir string) string {
	return filepath.Join(filepath.Dir(wsDir), "."+filepath.Base(wsDir)+".teardown-staged")
}

func teardownDeclinedMarker(wsDir string) string {
	return filepath.Join(filepath.Dir(wsDir), "."+filepath.Base(wsDir)+".teardown-declined")
}

// createMarkerFile writes a marker without ever following a symlink at the
// destination: remove whatever is there (Remove doesn't follow), then
// create-exclusive so a concurrently planted link makes the write fail
// loudly instead of truncating its target.
func createMarkerFile(path string, content []byte) error {
	_ = os.Remove(path)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if len(content) > 0 {
		if _, err := f.Write(content); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}

// insideOwnAgentSession reports whether this process runs inside a live
// agent session for this specific workspace: only then must teardown be
// staged rather than executed (the worktree is the session's own floor).
// Another workspace's teardown from in-session is safe to run directly.
func insideOwnAgentSession(name string) bool {
	return os.Getenv("SLATE_AGENT") == "1" && os.Getenv("SLATE_WORKSPACE") == name
}

func runDone(cmd *cobra.Command, args []string) error {
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
		return fmt.Errorf("workspace '%s' not found (already removed? `slate rm %s` cleans up leftovers)", name, name)
	}
	mainRoot, err := workspace.MainRoot()
	if err != nil {
		return err
	}

	ev := checkLanded(mainRoot, wsDir)
	if !ev.ok {
		fmt.Printf("%s is not finished:\n", name)
		for _, r := range ev.reasons {
			fmt.Printf("  - %s\n", r)
		}
		fmt.Println("Land the work first, or use `slate rm` to remove it anyway.")
		return fmt.Errorf("work has not landed")
	}

	if insideOwnAgentSession(name) {
		if err := createMarkerFile(stagedTeardownMarker(wsDir), []byte(ev.tip+"\n")); err != nil {
			return err
		}
		fmt.Printf(tick()+" %s (%s)\n", name, ev.evidence)
		fmt.Println("Teardown staged; the workspace will be removed when this agent session exits.")
		return nil
	}

	fmt.Printf(tick()+" %s (%s)\n", name, ev.evidence)
	hostname, err := resolveHostname(name)
	if err != nil {
		return err
	}
	return destroyWorkspace(name, wsDir, hostname, false, ev.branchOverride(), true, &ev)
}
