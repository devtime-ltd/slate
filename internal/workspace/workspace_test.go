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

