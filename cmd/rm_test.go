package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	if err := os.MkdirAll(filepath.Join(dir, ".slate"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".slate", "agent-started"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	summary, dirty := dirtyWorktreeSummary(dir)
	if dirty || summary != "" {
		t.Errorf("expected clean (only generated .env.container and .slate/), got dirty=%v summary=%q", dirty, summary)
	}
}

// The summary must not follow status.showUntrackedFiles: "no" would hide
// untracked work, "all" would list .slate/ file by file.
func TestDirtyWorktreeSummaryIgnoresUntrackedFilesConfig(t *testing.T) {
	dir := t.TempDir()
	gitInitWorktree(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, ".slate"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".slate", "agent-started"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	landedGit(t, dir, "config", "status.showUntrackedFiles", "all")
	if summary, dirty := dirtyWorktreeSummary(dir); dirty || summary != "" {
		t.Errorf("under showUntrackedFiles=all, expected clean (only .slate/ files), got dirty=%v summary=%q", dirty, summary)
	}

	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	landedGit(t, dir, "config", "status.showUntrackedFiles", "no")
	if summary, dirty := dirtyWorktreeSummary(dir); !dirty || summary != "1 untracked" {
		t.Errorf("under showUntrackedFiles=no, expected '1 untracked' (real.txt), got dirty=%v summary=%q", dirty, summary)
	}
}

// A path's own whitespace is not the porcelain separator: ".slate " is a
// file, not slate's directory.
func TestParseStatusKeepsPathWhitespace(t *testing.T) {
	summary, dirty, err := parseStatus("?? .slate \n?? .slate/\n?? .env.container\n")
	if err != nil || !dirty || summary != "1 untracked" {
		t.Errorf("expected '1 untracked' (.slate with a trailing space), got dirty=%v summary=%q err=%v", dirty, summary, err)
	}
	// a plain file or link where slate's directory should be is not generated
	summary, dirty, err = parseStatus("?? .slate\n")
	if err != nil || !dirty || summary != "1 untracked" {
		t.Errorf("expected '1 untracked' (a file named .slate), got dirty=%v summary=%q err=%v", dirty, summary, err)
	}
}

// Uncommitted edits inside a submodule are work slate done must not destroy.
func TestDirtyWorktreeSummarySeesDirtySubmodules(t *testing.T) {
	sub := filepath.Join(t.TempDir(), "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitWorktree(t, sub)
	landedGit(t, sub, "checkout", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(sub, "f.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	landedGit(t, sub, "add", "f.txt")
	landedGit(t, sub, "commit", "-q", "-m", "base")
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitWorktree(t, repo)
	landedGit(t, repo, "checkout", "-q", "-b", "main")
	landedGit(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	landedGit(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "sub")
	landedGit(t, repo, "commit", "-q", "-m", "add sub")
	if _, dirty := dirtyWorktreeSummary(repo); dirty {
		t.Fatal("the scenario needs a clean start")
	}
	if err := os.WriteFile(filepath.Join(repo, "sub", "f.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if summary, dirty := dirtyWorktreeSummary(repo); !dirty {
		t.Errorf("an edit inside a submodule should read as dirty, got %q", summary)
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

// git leaves an untracked directory it cannot open out of the listing, with a
// warning and exit 0; it may hold work, so it counts as untracked. slate's
// own .slate/ does not.
func TestDirtyWorktreeSummarySeesUnopenableDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open any directory")
	}
	dir := t.TempDir()
	gitInitWorktree(t, dir)
	for _, name := range []string{"sealed", ".slate"} {
		sealed := filepath.Join(dir, name)
		if err := os.Mkdir(sealed, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sealed, "inner.txt"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(sealed, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(sealed, 0o700) })
	}
	summary, dirty := dirtyWorktreeSummary(dir)
	if !dirty || summary != "1 untracked" {
		t.Errorf("an unopenable directory should count as untracked, got dirty=%v %q", dirty, summary)
	}
	if err := os.Chmod(filepath.Join(dir, "sealed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "sealed")); err != nil {
		t.Fatal(err)
	}
	if summary, dirty := dirtyWorktreeSummary(dir); dirty {
		t.Errorf("slate's own .slate/ is not work, openable or not, got %q", summary)
	}
}

// slate rm only warns about uncommitted changes, so a status that cannot be
// taken must warn too rather than read as clean.
func TestDirtyWorktreeSummaryFailsClosedOnAHugeListing(t *testing.T) {
	dir := t.TempDir()
	gitInitWorktree(t, dir)
	for i := 0; i < 8; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("untracked-%d.txt", i)), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	limit := gitOutputLimit
	gitOutputLimit = 16
	t.Cleanup(func() { gitOutputLimit = limit })
	summary, dirty := dirtyWorktreeSummary(dir)
	if !dirty || !strings.Contains(summary, "could not verify") {
		t.Errorf("an unjudgeable status should warn, got dirty=%v %q", dirty, summary)
	}
}

// A directory name holding a newline spreads git's warning over two lines
// and the directory is left out of the listing; the status refuses to read
// as clean, so teardown stays refused.
func TestDirtyWorktreeSummaryFailsClosedOnAnUnreadableWarning(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open any directory")
	}
	dir := t.TempDir()
	gitInitWorktree(t, dir)
	sealed := filepath.Join(dir, "bad\nname")
	if err := os.Mkdir(sealed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sealed, "inner.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sealed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(sealed, 0o700) })
	summary, dirty := dirtyWorktreeSummary(dir)
	if !dirty || !strings.Contains(summary, "could not verify") {
		t.Errorf("a warning that cannot be read should refuse the status, got dirty=%v %q", dirty, summary)
	}
	if _, _, err := worktreeStatusFor("", dir); !errors.Is(err, errUnreadWarning) {
		t.Errorf("the teardown status should carry the refusal, got %v", err)
	}
}
