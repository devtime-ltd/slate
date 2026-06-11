package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitInitWorktree(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// A pristine slate workspace always has an untracked .env.container, which must
// not be reported as uncommitted work (otherwise `slate rm` warns every time).
func TestDirtyWorktreeSummaryIgnoresUntrackedEnvContainer(t *testing.T) {
	dir := t.TempDir()
	gitInitWorktree(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".env.container"), []byte("X=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	summary, dirty := dirtyWorktreeSummary(dir)
	if dirty || summary != "" {
		t.Errorf("expected clean (only generated .env.container), got dirty=%v summary=%q", dirty, summary)
	}
}

// Real untracked work alongside .env.container must still be counted.
func TestDirtyWorktreeSummaryCountsOtherUntracked(t *testing.T) {
	dir := t.TempDir()
	gitInitWorktree(t, dir)
	os.WriteFile(filepath.Join(dir, ".env.container"), []byte("X=1\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "real.txt"), []byte("work\n"), 0o644)
	summary, dirty := dirtyWorktreeSummary(dir)
	if !dirty || summary != "1 untracked" {
		t.Errorf("expected '1 untracked' (real.txt), got dirty=%v summary=%q", dirty, summary)
	}
}

// If .env.container is somehow tracked and then modified, that is real work and
// must be counted (the skip applies only while it is untracked).
func TestDirtyWorktreeSummaryCountsModifiedTrackedEnvContainer(t *testing.T) {
	dir := t.TempDir()
	gitInitWorktree(t, dir)
	p := filepath.Join(dir, ".env.container")
	os.WriteFile(p, []byte("X=1\n"), 0o644)
	for _, args := range [][]string{{"add", ".env.container"}, {"commit", "-q", "-m", "add"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.WriteFile(p, []byte("X=2\n"), 0o644)
	summary, dirty := dirtyWorktreeSummary(dir)
	if !dirty || summary != "1 modified" {
		t.Errorf("expected '1 modified' for tracked+modified .env.container, got dirty=%v summary=%q", dirty, summary)
	}
}
