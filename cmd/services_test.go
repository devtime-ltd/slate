package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceConfigNote(t *testing.T) {
	mainRoot := t.TempDir()
	wsDir := t.TempDir()
	write := func(dir, content string) {
		if err := os.WriteFile(filepath.Join(dir, "slate.yml"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// no workspace copy: nothing to compare
	write(mainRoot, "scaffold: laravel\n")
	if note := workspaceConfigNote(mainRoot, wsDir); note != "" {
		t.Errorf("no ws slate.yml: want no note, got %q", note)
	}

	// identical (modulo trailing whitespace): quiet
	write(wsDir, "scaffold: laravel")
	if note := workspaceConfigNote(mainRoot, wsDir); note != "" {
		t.Errorf("identical configs: want no note, got %q", note)
	}

	// edited in the worktree only: note which config wins
	write(wsDir, "scaffold: laravel\nagent: claude\n")
	note := workspaceConfigNote(mainRoot, wsDir)
	if note == "" {
		t.Fatal("differing configs: want a note")
	}
	if !strings.Contains(note, "workspace's slate.yml") {
		t.Errorf("note should say the workspace config is in use, got %q", note)
	}
}

func TestProvisioningLockCleanupTombstone(t *testing.T) {
	wsDir := t.TempDir()
	slateDir := filepath.Join(wsDir, ".slate")
	if err := os.MkdirAll(slateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cleanup := writeProvisioningLock(wsDir)

	// deny dir writes so the lock can't be unlinked, only rewritten in place
	if err := os.Chmod(slateDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(slateDir, 0o755) })

	cleanup(nil)
	if pid, alive := readProvisioningLock(wsDir); pid != 0 || alive {
		t.Errorf("tombstoned lock should read as no lock, got pid=%d alive=%v", pid, alive)
	}
	data, err := os.ReadFile(filepath.Join(slateDir, "provisioning"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "0" {
		t.Errorf("want pid-0 tombstone, got %q", data)
	}
}
