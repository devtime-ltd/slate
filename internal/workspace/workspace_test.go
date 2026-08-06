package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestValidateName(t *testing.T) {
	valid := []string{"a", "foo", "fix-bug", "feat123", "a1"}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []struct {
		name string
		desc string
	}{
		{"", "empty"},
		{"A", "uppercase"},
		{"foo_bar", "underscore"},
		{"-foo", "starts with dash"},
		{"foo-", "ends with dash"},
		{"1foo", "starts with digit"},
		{"abcdefghijklmnopqrstuvwxyz1234567", "too long (33 chars)"},
		{"main", "reserved"},
		{"master", "reserved"},
		{"default", "reserved"},
		{"all", "reserved"},
	}
	for _, tt := range invalid {
		t.Run(tt.desc, func(t *testing.T) {
			if err := ValidateName(tt.name); err == nil {
				t.Errorf("ValidateName(%q) = nil, want error", tt.name)
			}
		})
	}
}

func TestShortenName(t *testing.T) {
	tests := []struct {
		name string
		want string
		desc string
	}{
		{"festival-submission-listings-losing-filter-on-pagination", "festival-submission-listings", "cut mid-word drops the fragment"},
		{"abcdefghijklmnopqrstuvwxyz-abcde-xyz", "abcdefghijklmnopqrstuvwxyz-abcde", "cut lands exactly before a dash: kept whole"},
		{"abcdefghijklmnopqrstuvwxyzabcde-xyz", "abcdefghijklmnopqrstuvwxyzabcde", "cut ends on a dash: dash trimmed"},
		{"abcdefghijklmnopqrstuvwxyz1234567890", "abcdefghijklmnopqrstuvwxyz123456", "no dashes: raw truncation"},
		{"short-name", "", "not over-long"},
		{"ABCDEFGHIJKLMNOPQRSTUVWXYZ-ABCDEF-XYZ", "", "invalid beyond length: no suggestion"},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			if got := ShortenName(tt.name); got != tt.want {
				t.Errorf("ShortenName(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestResolveWorkspace(t *testing.T) {
	tmp := t.TempDir()
	SetMainRootOverride(tmp)
	t.Cleanup(func() { SetMainRootOverride(""); SetWorkspaceOverride("") })

	// Override pointing at a non-existent workspace -> not found.
	SetWorkspaceOverride("ghost")
	if _, _, err := ResolveWorkspace(); err == nil {
		t.Error("expected error for missing workspace override")
	}

	// Override pointing at an existing workspace dir -> resolves.
	wsDir := filepath.Join(tmp, ".slate", "workspaces", "foo")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	SetWorkspaceOverride("foo")
	name, dir, err := ResolveWorkspace()
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	if name != "foo" || dir != wsDir {
		t.Errorf("ResolveWorkspace() = (%q, %q), want (foo, %q)", name, dir, wsDir)
	}

	// Invalid override name -> validation error.
	SetWorkspaceOverride("BadName")
	if _, _, err := ResolveWorkspace(); err == nil {
		t.Error("expected validation error for invalid override name")
	}
}

func TestAdoptDirtyChanges(t *testing.T) {
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	main := t.TempDir()
	git(main, "init", "-q")
	git(main, "config", "user.email", "t@example.com")
	git(main, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(main, "tracked.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(main, "add", ".")
	git(main, "commit", "-qm", "init")

	// Dirty the main checkout: edit a tracked file and add an untracked one.
	if err := os.WriteFile(filepath.Join(main, "tracked.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wt := filepath.Join(t.TempDir(), "wt")
	git(main, "worktree", "add", "-q", wt, "HEAD")

	adopted, err := AdoptDirtyChanges(main, wt)
	if err != nil {
		t.Fatalf("AdoptDirtyChanges: %v", err)
	}
	if !adopted {
		t.Fatal("expected adopted = true")
	}

	if b, _ := os.ReadFile(filepath.Join(wt, "tracked.txt")); string(b) != "one\ntwo\n" {
		t.Errorf("tracked change not applied in worktree: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "untracked.txt")); string(b) != "new\n" {
		t.Errorf("untracked file not copied to worktree: %q", b)
	}
	// Main checkout left intact (its tracked.txt is still the dirty version).
	if b, _ := os.ReadFile(filepath.Join(main, "tracked.txt")); string(b) != "one\ntwo\n" {
		t.Errorf("main checkout was modified: %q", b)
	}
}

func TestValidateNameMaxLength(t *testing.T) {
	name32 := "abcdefghijklmnopqrstuvwxyz123456"
	if len(name32) != 32 {
		t.Fatalf("test name is %d chars, want 32", len(name32))
	}
	if err := ValidateName(name32); err != nil {
		t.Errorf("32-char name should be valid: %v", err)
	}

	name33 := name32 + "7"
	if err := ValidateName(name33); err == nil {
		t.Error("33-char name should be invalid")
	}
}

func TestWorktreeListed(t *testing.T) {
	porcelain := `worktree /repo/main
HEAD abc123
branch refs/heads/main

worktree /repo/.slate/workspaces/this-week
HEAD def456
branch refs/heads/slate/this-week
prunable gitdir file points to non-existent location
`
	if !worktreeListed(porcelain, "/repo/.slate/workspaces/this-week") {
		t.Error("expected registered worktree to be found")
	}
	if !worktreeListed(porcelain, "/repo/.slate/workspaces/this-week/") {
		t.Error("expected trailing-slash path to match after Clean")
	}
	if worktreeListed(porcelain, "/repo/.slate/workspaces/this") {
		t.Error("prefix of a registered path must not match")
	}
	if worktreeListed(porcelain, "/repo/.slate/workspaces/other") {
		t.Error("unregistered path must not match")
	}
}

func TestParseWorktrees(t *testing.T) {
	porcelain := `worktree /repo/main
HEAD abc123
branch refs/heads/main

worktree /repo/.slate/workspaces/gone
HEAD def456
branch refs/heads/slate/gone
prunable gitdir file points to non-existent location

worktree /repo/.slate/workspaces/detached
HEAD 789abc
detached
`
	entries := parseWorktrees(porcelain)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(entries), entries)
	}
	want := []worktreeEntry{
		{"/repo/main", "main"},
		{"/repo/.slate/workspaces/gone", "slate/gone"},
		{"/repo/.slate/workspaces/detached", ""},
	}
	for i, w := range want {
		if entries[i] != w {
			t.Errorf("entry %d = %+v, want %+v", i, entries[i], w)
		}
	}
}

func TestBranchSafety(t *testing.T) {
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	origin := t.TempDir()
	git(origin, "init", "-q", "--bare", "-b", "main")

	repo := t.TempDir()
	git(repo, "init", "-q", "-b", "main")
	git(repo, "config", "user.email", "t@example.com")
	git(repo, "config", "user.name", "t")

	commit := func(file string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, file), []byte(file+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(repo, "add", ".")
		git(repo, "commit", "-qm", file)
	}

	commit("base.txt")
	git(repo, "remote", "add", "origin", origin)
	git(repo, "push", "-qu", "origin", "main")

	git(repo, "checkout", "-qb", "slate/pushed")
	commit("a.txt")
	git(repo, "push", "-qu", "origin", "slate/pushed")

	git(repo, "checkout", "-qb", "slate/local", "main")
	commit("b.txt")

	git(repo, "checkout", "-qb", "slate/ahead", "main")
	commit("c.txt")
	git(repo, "push", "-qu", "origin", "slate/ahead")
	commit("c2.txt")

	git(repo, "checkout", "-qb", "slate/merged", "main")
	commit("d.txt")
	git(repo, "checkout", "-q", "main")
	git(repo, "merge", "-q", "--no-ff", "-m", "merge", "slate/merged")

	// Pushed, then the remote-tracking ref vanished (remote branch deleted).
	git(repo, "checkout", "-qb", "slate/gone", "main")
	commit("e.txt")
	git(repo, "push", "-qu", "origin", "slate/gone")
	git(repo, "update-ref", "-d", "refs/remotes/origin/slate/gone")

	git(repo, "checkout", "-q", "main")

	cases := []struct {
		branch string
		safe   bool
		reason string
	}{
		{"slate/pushed", true, "in sync with origin/slate/pushed"},
		{"slate/local", false, "never pushed"},
		{"slate/ahead", false, "unpushed commits"},
		{"slate/merged", true, "merged into main"},
		{"slate/gone", false, "upstream gone, possibly squash-merged"},
		{"slate/nonexistent", false, "no such branch"},
	}
	for _, c := range cases {
		safe, reason := BranchSafety(repo, c.branch)
		if safe != c.safe || reason != c.reason {
			t.Errorf("%s: got (%v, %q), want (%v, %q)", c.branch, safe, reason, c.safe, c.reason)
		}
	}
}
