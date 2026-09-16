package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/workspace"
)

// A tmux session or a later shell reaches `slate agent` with no SLATE_FRESH
// and, once provisioned, no bareness either; only the debt recorded at
// creation tells such an entry it is the first.
func TestCreateWorkspaceOwesFirstRunEntry(t *testing.T) {
	mainRoot := newTestProject(t)

	if err := createWorkspace("ws", "", baseSpec{}, false, false, false, true); err != nil {
		t.Fatalf("slate new --bare: %v", err)
	}

	wsDir := filepath.Join(mainRoot, ".slate", "workspaces", "ws")
	if _, err := os.Stat(firstRunPendingMarker(wsDir)); err != nil {
		t.Fatalf("a new workspace should be recorded as owing its first-run entry: %v", err)
	}

	// the signals an entry outside the hooks doesn't have
	t.Setenv("SLATE_FRESH", "")
	if err := os.Remove(unprovisionedMarker(wsDir)); err != nil {
		t.Fatal(err)
	}
	if !agentFresh(mainRoot, wsDir) {
		t.Error("a new workspace entered outside the hooks should still get the first-run variant")
	}
	if debt := readFirstRunDebt(wsDir); debt.Head != landedGitOut(t, wsDir, "rev-parse", "HEAD") || debt.Provisioning {
		t.Errorf("a bare workspace should record its HEAD as the baseline with no provisioning window, got %+v", debt)
	}
}

// --adopt carries the main checkout's changes in before the debt is recorded,
// so the first entry is still the first.
func TestCreateWorkspaceAdoptedChangesAreNotWork(t *testing.T) {
	mainRoot := newTestProject(t)
	if err := os.WriteFile(filepath.Join(mainRoot, "slate.yml"), []byte(generateSlateYml("laravel")+"\n# local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := createWorkspace("ws", "", baseSpec{}, false, false, true, true); err != nil {
		t.Fatalf("slate new --adopt --bare: %v", err)
	}
	wsDir := filepath.Join(mainRoot, ".slate", "workspaces", "ws")
	if _, dirty := dirtyWorktreeSummary(wsDir); !dirty {
		t.Fatal("the scenario needs the adopted edit present in the worktree")
	}
	t.Setenv("SLATE_FRESH", "")
	if err := os.Remove(unprovisionedMarker(wsDir)); err != nil {
		t.Fatal(err)
	}
	if !agentFresh(mainRoot, wsDir) {
		t.Error("adopted changes should not make a new workspace look worked")
	}
}

func newTestProject(t *testing.T) string {
	t.Helper()
	t.Setenv("SLATE_CONFIG_DIR", t.TempDir())
	t.Setenv("SLATE_DATA_DIR", t.TempDir())

	root := t.TempDir()
	gitInitWorktree(t, root)
	if err := os.WriteFile(filepath.Join(root, "slate.yml"), []byte(generateSlateYml("laravel")), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	workspace.SetMainRootOverride(root)
	t.Cleanup(func() { workspace.SetMainRootOverride("") })
	return root
}

// baseRepo: main with one commit, a remote-only `dev`, and HEAD parked on an
// unrelated branch so a HEAD-based fork is distinguishable from main.
func baseRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitInitWorktree(t, dir)
	landedGit(t, dir, "checkout", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644)
	landedGit(t, dir, "add", ".")
	landedGit(t, dir, "commit", "-q", "-m", "initial")
	landedGit(t, dir, "update-ref", "refs/remotes/origin/dev", "HEAD")
	landedGit(t, dir, "checkout", "-q", "-b", "unrelated")
	return dir
}

func TestResolveBase(t *testing.T) {
	repo := baseRepo(t)
	landedGit(t, repo, "branch", "slate/existing", "main")
	no, yes := false, true

	cases := []struct {
		name   string
		branch string
		spec   baseSpec
		cfg    config.ProjectConfig
		want   string
	}{
		{"defaults to the default branch", "slate/new", baseSpec{}, config.ProjectConfig{}, "main"},
		{"--base wins", "slate/new", baseSpec{ref: "unrelated"}, config.ProjectConfig{BaseBranch: "main"}, "unrelated"},
		{"base_branch names another branch", "slate/new", baseSpec{}, config.ProjectConfig{BaseBranch: "unrelated"}, "unrelated"},
		{"base_branch falls back to origin", "slate/new", baseSpec{}, config.ProjectConfig{BaseBranch: "dev"}, "origin/dev"},
		{"base_from_head keeps the old behaviour", "slate/new", baseSpec{}, config.ProjectConfig{BaseFromHead: true}, ""},
		{"--base-head overrides the config", "slate/new", baseSpec{head: &yes}, config.ProjectConfig{}, ""},
		{"--base-head=false overrides the config", "slate/new", baseSpec{head: &no}, config.ProjectConfig{BaseFromHead: true}, "main"},
		{"an existing branch gets no base", "slate/existing", baseSpec{}, config.ProjectConfig{}, ""},
	}
	for _, c := range cases {
		got, err := resolveBase(repo, c.branch, c.spec, c.cfg)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
		} else if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// The default branch is a best effort: a repo without one still creates
// workspaces, forking from wherever the main checkout sits.
func TestResolveBaseWithoutDefaultBranch(t *testing.T) {
	dir := t.TempDir()
	gitInitWorktree(t, dir)
	landedGit(t, dir, "checkout", "-q", "-b", "trunk")
	os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644)
	landedGit(t, dir, "add", ".")
	landedGit(t, dir, "commit", "-q", "-m", "initial")

	got, err := resolveBase(dir, "slate/new", baseSpec{}, config.ProjectConfig{})
	if err != nil || got != "" {
		t.Errorf("got (%q, %v), want the current HEAD and no error", got, err)
	}
}

// A configured base_branch that doesn't resolve is a typo or a missing fetch,
// not a licence to fork from an unrelated HEAD.
func TestResolveBaseRejectsUnknownRefs(t *testing.T) {
	repo := baseRepo(t)

	if _, err := resolveBase(repo, "slate/new", baseSpec{}, config.ProjectConfig{BaseBranch: "nope"}); err == nil || !strings.Contains(err.Error(), "base_branch") {
		t.Errorf("want base_branch 'nope' refused, got %v", err)
	}
	if _, err := resolveBase(repo, "slate/new", baseSpec{ref: "nope"}, config.ProjectConfig{}); err == nil || !strings.Contains(err.Error(), "--base") {
		t.Errorf("want --base nope refused, got %v", err)
	}
}
