package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/devtime-ltd/slate/internal/workspace"
)

// A tmux session or a later shell reaches `slate agent` with no SLATE_FRESH
// and, once provisioned, no bareness either; only the debt recorded at
// creation tells such an entry it is the first.
func TestCreateWorkspaceOwesFirstRunEntry(t *testing.T) {
	mainRoot := newTestProject(t)

	if err := createWorkspace("ws", "", "", false, false, false, true); err != nil {
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
	if !agentFresh(wsDir) {
		t.Error("a new workspace entered outside the hooks should still get the first-run variant")
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
