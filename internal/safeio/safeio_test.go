package safeio

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestWriteFileAtRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	dir, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()

	victim := filepath.Join(root, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAt(dir, "link", []byte("clobber"), 0o644); err == nil {
		t.Error("writing through a symlink must be refused")
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep" {
		t.Errorf("victim clobbered: %q", got)
	}
}

func TestOpenDirRefusesSymlinkedDir(t *testing.T) {
	root := t.TempDir()
	real := t.TempDir()
	if err := os.Symlink(real, filepath.Join(root, "d")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDir(filepath.Join(root, "d")); err == nil {
		t.Error("OpenDir must refuse a symlinked directory")
	}
}

// The point of the pinned fd: a swap of the directory path AFTER OpenDir must
// not redirect writes, because *at resolves against the fd's inode.
func TestWriteFileAtSurvivesDirSwap(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "slate")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := OpenDir(realDir)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()

	// attacker swaps the path: rename the real dir away (its inode survives) and
	// plant a symlink to a sensitive dir in its place
	evil := t.TempDir()
	moved := filepath.Join(root, "slate-moved")
	if err := os.Rename(realDir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(evil, realDir); err != nil {
		t.Fatal(err)
	}

	if err := WriteFileAt(dir, "f", []byte("x"), 0o644); err != nil {
		t.Fatalf("write via the pinned fd should still reach the original inode: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(evil, "f")); err == nil {
		t.Error("write followed the swapped-in symlink into the attacker's dir")
	}
	if _, err := os.Lstat(filepath.Join(moved, "f")); err != nil {
		t.Errorf("write should have landed in the original (renamed) inode: %v", err)
	}
}

func TestIONeverBlocksOnFIFO(t *testing.T) {
	root := t.TempDir()
	dir, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}

	done := make(chan bool, 1)
	go func() {
		done <- WriteFileAt(dir, "pipe", []byte("x"), 0o644) != nil
	}()
	select {
	case refused := <-done:
		if !refused {
			t.Error("a FIFO should be refused by WriteFileAt")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WriteFileAt blocked on a FIFO (host-side DoS)")
	}
}

func TestLeafValidation(t *testing.T) {
	root := t.TempDir()
	dir, _ := OpenDir(root)
	defer dir.Close()
	for _, bad := range []string{"", ".", "..", "a/b", "../escape"} {
		if err := WriteFileAt(dir, bad, nil, 0o644); err == nil {
			t.Errorf("name %q should be rejected", bad)
		}
	}
}

func TestRemoveAllAtRecursiveAndSymlinkSafe(t *testing.T) {
	root := t.TempDir()
	dir, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()

	// a nested tree under name
	if err := os.MkdirAll(filepath.Join(root, "d", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "d", "sub", "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAllAt(dir, "d"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "d")); err == nil {
		t.Error("d should be gone")
	}
	// missing is not an error
	if err := RemoveAllAt(dir, "d"); err != nil {
		t.Errorf("removing a missing name should be a no-op: %v", err)
	}

	// a symlinked name is unlinked, its target left intact
	victimDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(victimDir, "keep"), []byte("k"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victimDir, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAllAt(dir, "link"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "link")); err == nil {
		t.Error("the symlink should be removed")
	}
	if _, err := os.Stat(filepath.Join(victimDir, "keep")); err != nil {
		t.Error("the symlink target must be left intact")
	}
}

func TestMkdirAtCreatesDir(t *testing.T) {
	root := t.TempDir()
	dir, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := MkdirAt(dir, "x", 0o755); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(filepath.Join(root, "x")); err != nil || !info.IsDir() {
		t.Errorf("x should be a directory: %v", err)
	}
}

func TestWriteFileAtTruncatesExisting(t *testing.T) {
	root := t.TempDir()
	dir, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("longer-old-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAt(dir, "f", []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "f"))
	if string(got) != "new" {
		t.Errorf("content = %q, want new (old content must be truncated)", got)
	}
}
