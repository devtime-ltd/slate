package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// landedRepo builds a repo standing in for both the main checkout and a
// workspace worktree: an initial commit on main, then a slate/test branch
// checked out. Tests shape it from there.
func landedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitInitWorktree(t, dir)
	landedGit(t, dir, "checkout", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644)
	landedGit(t, dir, "add", ".")
	landedGit(t, dir, "commit", "-q", "-m", "initial")
	landedGit(t, dir, "checkout", "-q", "-b", "slate/test")
	return dir
}

func landedGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func landedGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func commitOnBranch(t *testing.T, dir string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("done\n"), 0o644)
	landedGit(t, dir, "add", ".")
	landedGit(t, dir, "commit", "-q", "-m", "feature")
}

// fakeGh front-runs PATH with a gh stub. Empty body means "gh fails"
// (missing, offline, no remote); otherwise the body is the JSON that
// `gh pr list` would print.
func fakeGh(t *testing.T, jsonBody string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\nexit 1\n"
	if jsonBody != "" {
		script = "#!/bin/sh\ncat <<'EOF'\n" + jsonBody + "\nEOF\n"
	}
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func reasonsContain(ev landedEvidence, substr string) bool {
	for _, r := range ev.reasons {
		if strings.Contains(r, substr) {
			return true
		}
	}
	return false
}

// A branch with no commits of its own is safe to finish, but must not read
// as landed *work* (that would make the agent exit hook offer teardown for
// every freshly created workspace).
func TestCheckLandedNoCommitsBeyondDefault(t *testing.T) {
	dir := landedRepo(t)
	fakeGh(t, "")
	ev := checkLanded(dir, dir)
	if !ev.ok || ev.hasWork {
		t.Errorf("expected ok && !hasWork, got ok=%v hasWork=%v reasons=%v", ev.ok, ev.hasWork, ev.reasons)
	}
	if !strings.Contains(ev.evidence, "no commits beyond main") {
		t.Errorf("evidence = %q", ev.evidence)
	}
}

func TestCheckLandedRefusesDirtyWorktree(t *testing.T) {
	dir := landedRepo(t)
	fakeGh(t, "")
	os.WriteFile(filepath.Join(dir, "wip.txt"), []byte("wip\n"), 0o644)
	ev := checkLanded(dir, dir)
	if ev.ok {
		t.Errorf("expected refusal for dirty worktree, got evidence=%q", ev.evidence)
	}
	if !reasonsContain(ev, "uncommitted changes") {
		t.Errorf("reasons = %v", ev.reasons)
	}
}

// A locally merged branch whose PR is recorded as merged is landed work.
func TestCheckLandedMergedBranchWithMergedPR(t *testing.T) {
	dir := landedRepo(t)
	commitOnBranch(t, dir)
	tip := landedGitOut(t, dir, "rev-parse", "HEAD")
	landedGit(t, dir, "checkout", "-q", "main")
	landedGit(t, dir, "merge", "-q", "--no-ff", "--no-edit", "slate/test")
	landedGit(t, dir, "checkout", "-q", "slate/test")
	fakeGh(t, `[{"number":42,"headRefName":"slate/test","headRefOid":"`+tip+`","baseRefName":"main","state":"MERGED","url":""}]`)

	ev := checkLanded(dir, dir)
	if !ev.ok || !ev.hasWork {
		t.Errorf("expected ok && hasWork, got ok=%v hasWork=%v reasons=%v", ev.ok, ev.hasWork, ev.reasons)
	}
	if !strings.Contains(ev.evidence, "PR #42 merged into main") {
		t.Errorf("evidence = %q", ev.evidence)
	}
}

// Merged by ancestry but with no PR to vouch for it: safe to finish, but not
// something the exit hook should volunteer about.
func TestCheckLandedMergedBranchWithoutPR(t *testing.T) {
	dir := landedRepo(t)
	commitOnBranch(t, dir)
	landedGit(t, dir, "checkout", "-q", "main")
	landedGit(t, dir, "merge", "-q", "--no-ff", "--no-edit", "slate/test")
	landedGit(t, dir, "checkout", "-q", "slate/test")
	fakeGh(t, "")

	ev := checkLanded(dir, dir)
	if !ev.ok || ev.hasWork {
		t.Errorf("expected ok && !hasWork, got ok=%v hasWork=%v reasons=%v", ev.ok, ev.hasWork, ev.reasons)
	}
}

// The rebase-merge case: ancestry can't see the merge (GitHub rewrote the
// SHAs), but the PR merged and its recorded head matches the local tip.
func TestCheckLandedRebaseMergedViaPR(t *testing.T) {
	dir := landedRepo(t)
	commitOnBranch(t, dir)
	tip := landedGitOut(t, dir, "rev-parse", "HEAD")
	fakeGh(t, `[{"number":7,"headRefName":"slate/test","headRefOid":"`+tip+`","baseRefName":"main","state":"MERGED","url":""}]`)

	ev := checkLanded(dir, dir)
	if !ev.ok || !ev.hasWork {
		t.Errorf("expected ok && hasWork, got ok=%v hasWork=%v reasons=%v", ev.ok, ev.hasWork, ev.reasons)
	}
	if !strings.Contains(ev.evidence, "PR #7 merged (local tip matches)") {
		t.Errorf("evidence = %q", ev.evidence)
	}
}

// Commits added after the PR merged must refuse: they are not what landed.
func TestCheckLandedRefusesTipBeyondMergedPR(t *testing.T) {
	dir := landedRepo(t)
	commitOnBranch(t, dir)
	fakeGh(t, `[{"number":7,"headRefName":"slate/test","headRefOid":"0000000000000000000000000000000000000000","baseRefName":"main","state":"MERGED","url":""}]`)

	ev := checkLanded(dir, dir)
	if ev.ok {
		t.Errorf("expected refusal, got evidence=%q", ev.evidence)
	}
	if !reasonsContain(ev, "moved beyond what merged") {
		t.Errorf("reasons = %v", ev.reasons)
	}
}

func TestCheckLandedRefusesOpenPR(t *testing.T) {
	dir := landedRepo(t)
	commitOnBranch(t, dir)
	fakeGh(t, `[{"number":9,"headRefName":"slate/test","headRefOid":"x","state":"OPEN","url":""}]`)

	ev := checkLanded(dir, dir)
	if ev.ok {
		t.Errorf("expected refusal, got evidence=%q", ev.evidence)
	}
	if !reasonsContain(ev, "PR #9 is still open") {
		t.Errorf("reasons = %v", ev.reasons)
	}
}

// Unmerged commits with no PR must refuse, even when the worktree is clean.
func TestCheckLandedRefusesUnmergedWork(t *testing.T) {
	dir := landedRepo(t)
	commitOnBranch(t, dir)
	fakeGh(t, "")

	ev := checkLanded(dir, dir)
	if ev.ok {
		t.Errorf("expected refusal for unmerged work, got evidence=%q", ev.evidence)
	}
	if !reasonsContain(ev, "not merged into main") {
		t.Errorf("reasons = %v", ev.reasons)
	}
}

func TestCheckLandedRefusesDetachedHead(t *testing.T) {
	dir := landedRepo(t)
	fakeGh(t, "")
	landedGit(t, dir, "checkout", "-q", "--detach")
	ev := checkLanded(dir, dir)
	if ev.ok {
		t.Errorf("expected refusal for detached HEAD, got evidence=%q", ev.evidence)
	}
	if !reasonsContain(ev, "detached HEAD") {
		t.Errorf("reasons = %v", ev.reasons)
	}
}

// A dirty worktree on a merged branch must report the dirt, not tear down.
func TestCheckLandedMergedButDirty(t *testing.T) {
	dir := landedRepo(t)
	commitOnBranch(t, dir)
	landedGit(t, dir, "checkout", "-q", "main")
	landedGit(t, dir, "merge", "-q", "--no-ff", "--no-edit", "slate/test")
	landedGit(t, dir, "checkout", "-q", "slate/test")
	fakeGh(t, "")
	os.WriteFile(filepath.Join(dir, "after.txt"), []byte("more\n"), 0o644)

	ev := checkLanded(dir, dir)
	if ev.ok {
		t.Errorf("expected refusal for merged-but-dirty, got evidence=%q", ev.evidence)
	}
	if !reasonsContain(ev, "uncommitted changes") {
		t.Errorf("reasons = %v", ev.reasons)
	}
}

// A PR merged into a non-default base (a stacked PR) is not landed work.
func TestCheckLandedRefusesPRIntoNonDefaultBase(t *testing.T) {
	dir := landedRepo(t)
	commitOnBranch(t, dir)
	tip := landedGitOut(t, dir, "rev-parse", "HEAD")
	fakeGh(t, `[{"number":11,"headRefName":"slate/test","headRefOid":"`+tip+`","baseRefName":"feature/parent","state":"MERGED","url":""}]`)

	ev := checkLanded(dir, dir)
	if ev.ok {
		t.Errorf("expected refusal for non-default base, got evidence=%q", ev.evidence)
	}
	if !reasonsContain(ev, "not main") {
		t.Errorf("reasons = %v", ev.reasons)
	}
}

// gh being unable to answer must not read as "no PR": the refusal has to
// say the check itself failed.
func TestCheckLandedDistinguishesGhFailureFromNoPR(t *testing.T) {
	dir := landedRepo(t)
	commitOnBranch(t, dir)
	fakeGh(t, "")

	ev := checkLanded(dir, dir)
	if ev.ok {
		t.Errorf("expected refusal, got evidence=%q", ev.evidence)
	}
	if !reasonsContain(ev, "gh could not check") {
		t.Errorf("reasons = %v", ev.reasons)
	}
}

// done must never judge (and destroyWorkspace must never delete) the
// default branch itself.
func TestCheckLandedRefusesDefaultBranch(t *testing.T) {
	dir := landedRepo(t)
	fakeGh(t, "")
	landedGit(t, dir, "checkout", "-q", "main")

	ev := checkLanded(dir, dir)
	if ev.ok {
		t.Errorf("expected refusal on the default branch, got evidence=%q", ev.evidence)
	}
	if !reasonsContain(ev, "default branch") {
		t.Errorf("reasons = %v", ev.reasons)
	}
}

// A git-status failure must fail closed: no evidence, an honest reason.
func TestCheckLandedFailsClosedWhenStatusFails(t *testing.T) {
	dir := t.TempDir() // not a git repo: git status errors here
	fakeGh(t, "")
	ev := checkLanded(dir, dir)
	if ev.ok {
		t.Errorf("expected refusal when git status fails, got evidence=%q", ev.evidence)
	}
	if !reasonsContain(ev, "could not verify the worktree is clean") {
		t.Errorf("reasons = %v", ev.reasons)
	}
}

// Ancestry-only evidence must not authorise branch deletion; PR-proven
// evidence must.
func TestBranchOverrideRequiresPRProof(t *testing.T) {
	anc := landedEvidence{ok: true, evidence: "nothing beyond main · worktree clean"}
	if anc.branchOverride() != "" {
		t.Errorf("ancestry-only evidence must not override BranchSafety")
	}
	pr := landedEvidence{ok: true, prProven: true, evidence: "PR #7 merged (local tip matches) · worktree clean"}
	if pr.branchOverride() == "" {
		t.Errorf("PR-proven evidence should override BranchSafety")
	}
}

// A dirty worktree is a decided refusal: gh must not be consulted, and no
// PR-based reason may stack onto the dirty one.
func TestCheckLandedSkipsGhWhenAlreadyRefusing(t *testing.T) {
	dir := landedRepo(t)
	commitOnBranch(t, dir)
	tip := landedGitOut(t, dir, "rev-parse", "HEAD")
	// A gh stub that would prove the merge; reaching it would flip the result.
	fakeGh(t, `[{"number":7,"headRefName":"slate/test","headRefOid":"`+tip+`","baseRefName":"main","state":"MERGED","url":""}]`)
	os.WriteFile(filepath.Join(dir, "wip.txt"), []byte("wip\n"), 0o644)

	ev := checkLanded(dir, dir)
	if ev.ok {
		t.Errorf("expected refusal for dirty worktree, got evidence=%q", ev.evidence)
	}
	if len(ev.reasons) != 1 || !reasonsContain(ev, "uncommitted changes") {
		t.Errorf("expected the dirty reason alone, got %v", ev.reasons)
	}
}

// The teardown markers must live beside the worktree in the host-only
// workspaces root, never inside the container-writable worktree.
func TestTeardownMarkersLiveOutsideTheWorktree(t *testing.T) {
	ws := "/proj/.slate/workspaces/api"
	for _, p := range []string{stagedTeardownMarker(ws), teardownDeclinedMarker(ws)} {
		if strings.HasPrefix(p, ws+string(filepath.Separator)) {
			t.Errorf("marker %q is inside the worktree", p)
		}
		if filepath.Dir(p) != "/proj/.slate/workspaces" {
			t.Errorf("marker %q not a sibling of the worktree", p)
		}
	}
}

// An exact-tip merged PR must win over a newer open PR on the same branch.
func TestCheckLandedMergedPRNotMaskedByOpenPR(t *testing.T) {
	dir := landedRepo(t)
	commitOnBranch(t, dir)
	tip := landedGitOut(t, dir, "rev-parse", "HEAD")
	fakeGh(t, `[{"number":21,"headRefName":"slate/test","headRefOid":"other","baseRefName":"main","state":"OPEN","url":""},{"number":20,"headRefName":"slate/test","headRefOid":"`+tip+`","baseRefName":"main","state":"MERGED","url":""}]`)

	ev := checkLanded(dir, dir)
	if !ev.ok || !ev.prProven {
		t.Errorf("expected merged exact-tip PR to win over the open PR, got ok=%v reasons=%v", ev.ok, ev.reasons)
	}
}

// createMarkerFile must refuse to write through a planted symlink.
func TestCreateMarkerFileDoesNotFollowSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "victim")
	os.WriteFile(target, []byte("precious\n"), 0o644)
	marker := filepath.Join(dir, "marker")

	// createMarkerFile removes the path first, so a pre-existing symlink is
	// deleted (not followed) and the victim survives with content intact.
	os.Symlink(target, marker)
	if err := createMarkerFile(marker, []byte("x")); err != nil {
		t.Fatalf("createMarkerFile: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "precious\n" {
		t.Errorf("symlink target was clobbered: %q", got)
	}
	if fi, err := os.Lstat(marker); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("marker was not replaced with a regular file")
	}
}

// A reused branch name whose older incarnation had a merged PR must not
// inherit that PR as evidence: the recorded head has to be this tip.
func TestCheckLandedIgnoresStaleMergedPROnReusedName(t *testing.T) {
	dir := landedRepo(t)
	// Branch sits strictly behind main with no commits of its own.
	commitOnBranch(t, dir)
	landedGit(t, dir, "checkout", "-q", "main")
	landedGit(t, dir, "merge", "-q", "--no-ff", "--no-edit", "slate/test")
	landedGit(t, dir, "checkout", "-q", "slate/test")
	landedGit(t, dir, "reset", "-q", "--hard", "HEAD~1")
	fakeGh(t, `[{"number":30,"headRefName":"slate/test","headRefOid":"oldincarnation","baseRefName":"main","state":"MERGED","url":""}]`)

	ev := checkLanded(dir, dir)
	if !ev.ok {
		t.Fatalf("expected safe, got reasons=%v", ev.reasons)
	}
	if ev.hasWork || ev.prProven {
		t.Errorf("stale merged PR must not count as landed work: hasWork=%v prProven=%v evidence=%q", ev.hasWork, ev.prProven, ev.evidence)
	}
}

// The security fix: landed checks anchor to the main checkout's read-only
// worktree registration, so a container swapping the worktree's `.git` pointer
// for a fake repo can't redirect them. Here the anchored HEAD is the real tip;
// the unanchored one follows the swap to the fake's tip.
func TestRegisteredGitDirAnchorsPastSwappedGit(t *testing.T) {
	mainRoot := t.TempDir()
	gitInitWorktree(t, mainRoot)
	landedGit(t, mainRoot, "checkout", "-q", "-b", "main")
	os.WriteFile(filepath.Join(mainRoot, "base.txt"), []byte("base\n"), 0o644)
	landedGit(t, mainRoot, "add", ".")
	landedGit(t, mainRoot, "commit", "-q", "-m", "initial")

	wsDir := filepath.Join(t.TempDir(), "ws")
	landedGit(t, mainRoot, "worktree", "add", "-q", "-b", "slate/test", wsDir)
	os.WriteFile(filepath.Join(wsDir, "feature.txt"), []byte("real\n"), 0o644)
	landedGit(t, wsDir, "add", ".")
	landedGit(t, wsDir, "commit", "-q", "-m", "real work")
	realTip := landedGitOut(t, wsDir, "rev-parse", "HEAD")

	gitDir, ok := registeredGitDir(mainRoot, wsDir)
	if !ok {
		t.Fatal("a real linked worktree must resolve to a registered git dir")
	}
	if !strings.Contains(gitDir, filepath.Join(".git", "worktrees")) {
		t.Errorf("git dir should live under the main checkout's .git/worktrees, got %q", gitDir)
	}

	// attacker swaps wsDir/.git for a fake repo with a different HEAD
	fake := t.TempDir()
	gitInitWorktree(t, fake)
	landedGit(t, fake, "checkout", "-q", "-b", "slate/test")
	os.WriteFile(filepath.Join(fake, "x"), []byte("x"), 0o644)
	landedGit(t, fake, "add", ".")
	landedGit(t, fake, "commit", "-q", "-m", "fake")
	fakeTip := landedGitOut(t, fake, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(wsDir, ".git"), []byte("gitdir: "+filepath.Join(fake, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	anchored, err := gitFor(gitDir, wsDir, "rev-parse", "HEAD")
	if err != nil || anchored != realTip {
		t.Errorf("anchored HEAD = %q (err %v), want the real tip %q", anchored, err, realTip)
	}
	if unanchored, _ := gitFor("", wsDir, "rev-parse", "HEAD"); unanchored != fakeTip {
		t.Errorf("sanity: unanchored HEAD should follow the swapped .git to %q, got %q", fakeTip, unanchored)
	}

	// an unregistered dir has no registration to anchor to
	if _, ok := registeredGitDir(mainRoot, t.TempDir()); ok {
		t.Error("an unrelated dir must not resolve as registered")
	}
}

// checkLanded must resolve and record the main-anchored git dir for a real
// registered worktree; an empty ev.gitDir means it fell back to the worktree's
// own (swappable) .git - the regression this guards against.
func TestCheckLandedAnchorsRegisteredWorktree(t *testing.T) {
	mainRoot := t.TempDir()
	gitInitWorktree(t, mainRoot)
	landedGit(t, mainRoot, "checkout", "-q", "-b", "main")
	os.WriteFile(filepath.Join(mainRoot, "base.txt"), []byte("base\n"), 0o644)
	landedGit(t, mainRoot, "add", ".")
	landedGit(t, mainRoot, "commit", "-q", "-m", "initial")

	wsDir := filepath.Join(t.TempDir(), "ws")
	landedGit(t, mainRoot, "worktree", "add", "-q", "-b", "slate/test", wsDir)

	ev := checkLanded(mainRoot, wsDir)
	if ev.gitDir == "" {
		t.Error("checkLanded must anchor a registered worktree to the main checkout's git dir")
	}
	if !strings.Contains(ev.gitDir, filepath.Join(".git", "worktrees")) {
		t.Errorf("anchor should be under .git/worktrees, got %q", ev.gitDir)
	}
}

// gh working but returning no PR for the branch must refuse with "no PR found",
// distinct from a gh failure (offline / missing).
func TestCheckLandedNoPRFoundIsDistinctFromGhFailure(t *testing.T) {
	mainRoot := t.TempDir()
	gitInitWorktree(t, mainRoot)
	landedGit(t, mainRoot, "checkout", "-q", "-b", "main")
	os.WriteFile(filepath.Join(mainRoot, "base.txt"), []byte("base\n"), 0o644)
	landedGit(t, mainRoot, "add", ".")
	landedGit(t, mainRoot, "commit", "-q", "-m", "initial")

	wsDir := filepath.Join(t.TempDir(), "ws")
	landedGit(t, mainRoot, "worktree", "add", "-q", "-b", "slate/test", wsDir)
	os.WriteFile(filepath.Join(wsDir, "feature.txt"), []byte("work\n"), 0o644)
	landedGit(t, wsDir, "add", ".")
	landedGit(t, wsDir, "commit", "-q", "-m", "unmerged work")

	fakeGh(t, "[]") // gh works, returns no PRs

	ev := checkLanded(mainRoot, wsDir)
	if ev.ok {
		t.Error("unmerged branch with no PR must not be landed")
	}
	if !reasonsContain(ev, "no PR found") {
		t.Errorf("should say no PR found (not a gh failure); reasons=%v", ev.reasons)
	}
}
