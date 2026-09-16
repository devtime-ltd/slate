package safeio

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
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

func TestReadFileAtRefusesLinksAndFIFOs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "deep", "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()

	if got, err := ReadFileAt(dir, "sub/deep/file.txt", 5); err != nil || string(got) != "hello" {
		t.Errorf("nested regular file: got %q, %v", got, err)
	}
	if _, err := ReadFileAt(dir, "sub/deep/file.txt", 4); err == nil {
		t.Error("a file over the limit should be refused")
	}
	if _, err := ReadFileAt(dir, "link.txt", 1<<20); err == nil {
		t.Error("a symlink leaf should be refused")
	}
	if _, err := ReadFileAt(dir, "linkdir/outside.txt", 1<<20); err == nil {
		t.Error("a symlinked directory on the path should be refused")
	}
	done := make(chan error, 1)
	go func() { _, err := ReadFileAt(dir, "fifo", 1<<20); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a FIFO should be refused")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO must not block")
	}
}

func TestRegularFileAtIgnoresLinksAndDirs(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "file"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	for name, want := range map[string]bool{"file": true, "link": false, "dir": false, "missing": false, "../file": false} {
		if got := RegularFileAt(dir, name); got != want {
			t.Errorf("RegularFileAt(%q) = %v, want %v", name, got, want)
		}
	}
	for name, want := range map[string]bool{"file": true, "link": true, "dir": true, "missing": false, "../file": false} {
		if got := ExistsAt(dir, name); got != want {
			t.Errorf("ExistsAt(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestStatAtAndReadlinkAtNeverFollow(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	dir, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()

	if st, err := StatAt(dir, "sub/file.txt"); err != nil || st.Size != 5 {
		t.Errorf("StatAt(sub/file.txt): size %d, %v", st.Size, err)
	}
	if st, err := StatAt(dir, "link.txt"); err != nil || st.Mode&unix.S_IFMT != unix.S_IFLNK {
		t.Errorf("StatAt(link.txt) should report the link itself, got mode %o, %v", st.Mode&unix.S_IFMT, err)
	}
	if _, err := StatAt(dir, "linkdir/outside.txt"); err == nil {
		t.Error("StatAt through a symlinked directory should be refused")
	}
	if target, err := ReadlinkAt(dir, "link.txt"); err != nil || target != outside {
		t.Errorf("ReadlinkAt(link.txt) = %q, %v", target, err)
	}
	if _, err := ReadlinkAt(dir, "linkdir/outside.txt"); err == nil {
		t.Error("ReadlinkAt through a symlinked directory should be refused")
	}
}

func TestReplaceFileAtPublishesWholeContent(t *testing.T) {
	root := t.TempDir()
	dir, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := ReplaceFileAt(dir, "marker", []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceFileAt(dir, "marker", []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "marker"))
	if err != nil || string(got) != "second" {
		t.Errorf("marker = %q, %v", got, err)
	}
	if left, _ := filepath.Glob(filepath.Join(root, "marker.*.tmp")); len(left) != 0 {
		t.Errorf("the sibling should be gone after the rename, found %v", left)
	}
}

// The sibling belongs to the call: something planted at a predictable name
// beside the marker neither blocks the publish nor is written through.
func TestReplaceFileAtOwnsItsSibling(t *testing.T) {
	root := t.TempDir()
	dir, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := unix.Mkfifo(filepath.Join(root, "marker.tmp"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceFileAt(dir, "marker", []byte("published"), 0o644); err != nil {
		t.Fatalf("a FIFO beside the marker must not block the publish: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "marker"))
	if err != nil || string(got) != "published" {
		t.Errorf("marker = %q, %v", got, err)
	}
	if info, err := os.Lstat(filepath.Join(root, "marker.tmp")); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("the planted FIFO should be untouched, got %v %v", info, err)
	}
}
