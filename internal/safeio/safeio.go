// Package safeio does file I/O into container-writable workspace directories
// without a TOCTOU window. slate bind-mounts a workspace's worktree (.slate
// included) read-write into its containers, so container code can swap any
// path for a symlink between a check and a use. The defence is to pin the
// target directory as an fd once (OpenDir refuses a symlinked directory at
// open time) and address every file under it with *at syscalls, which resolve
// relative to that fd's inode and never re-walk the path a concurrent swap
// could redirect.
package safeio

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// OpenDir pins dir as an fd. O_NOFOLLOW refuses a symlink planted in dir's
// place; O_DIRECTORY refuses a non-directory. Close it when done.
func OpenDir(dir string) (*os.File, error) {
	return os.OpenFile(dir, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
}

// OpenDirAt pins name as a directory fd relative to parent, refusing a symlink
// or non-directory at name. Use it to descend into a subdirectory of an
// already-pinned dir without re-walking the path.
func OpenDirAt(parent *os.File, name string) (*os.File, error) {
	if err := checkLeaf(name); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(parent.Fd()), name,
		unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

// WriteFileAt writes data to name directly under dir. O_NOFOLLOW refuses a
// symlink at name, O_NONBLOCK keeps a planted FIFO/device from blocking the
// open, and the post-open regular-file check refuses anything that is not an
// ordinary file. name must be a single path element.
func WriteFileAt(dir *os.File, name string, data []byte, perm os.FileMode) (err error) {
	if err := checkLeaf(name); err != nil {
		return err
	}
	// No O_TRUNC on the open: truncation is deferred until after the target is
	// confirmed to be a regular file, so it can never take effect on a planted
	// FIFO or device.
	fd, err := unix.Openat(int(dir.Fd()), name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, uint32(perm))
	if err != nil {
		return fmt.Errorf("opening %s for writing (a symlink here is refused): %w", name, err)
	}
	f := os.NewFile(uintptr(fd), name)
	// Close propagates deferred write errors (e.g. on a networked filesystem);
	// keep it unless an earlier error already took precedence.
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file; refusing to write", name)
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	return writeFull(f, data)
}

// ReadFileAt reads rel through OpenFileAt, refusing a file larger than limit
// bytes so a container cannot make the host read something enormous.
func ReadFileAt(dir *os.File, rel string, limit int64) ([]byte, error) {
	f, err := OpenFileAt(dir, rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes; refusing to read", rel, limit)
	}
	return data, nil
}

// OpenFileAt opens rel, a slash-separated path under dir, for reading:
// descending each directory with OpenDirAt and opening the leaf with
// O_NOFOLLOW and O_NONBLOCK, so a symlink, FIFO or device planted anywhere on
// the path is refused rather than followed or blocked on. The caller closes
// the returned regular file.
func OpenFileAt(dir *os.File, rel string) (*os.File, error) {
	parent, leaf, done, err := descend(dir, rel)
	if err != nil {
		return nil, err
	}
	defer done()
	fd, err := unix.Openat(int(parent.Fd()), leaf,
		unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), leaf)
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a regular file; refusing to read", rel)
	}
	return f, nil
}

// StatAt stats rel under dir without following a symlink at any component;
// a symlink leaf is reported as itself.
func StatAt(dir *os.File, rel string) (unix.Stat_t, error) {
	var st unix.Stat_t
	parent, leaf, done, err := descend(dir, rel)
	if err != nil {
		return st, err
	}
	defer done()
	err = unix.Fstatat(int(parent.Fd()), leaf, &st, unix.AT_SYMLINK_NOFOLLOW)
	return st, err
}

// ReadlinkAt returns the target of the symlink at rel under dir, descending
// without following any component.
func ReadlinkAt(dir *os.File, rel string) (string, error) {
	parent, leaf, done, err := descend(dir, rel)
	if err != nil {
		return "", err
	}
	defer done()
	for size := 256; size <= 1<<16; size *= 2 {
		buf := make([]byte, size)
		n, err := unix.Readlinkat(int(parent.Fd()), leaf, buf)
		if err != nil {
			return "", err
		}
		if n < size {
			return string(buf[:n]), nil
		}
	}
	return "", fmt.Errorf("%s: link target too long", rel)
}

// descend pins the directory holding rel's leaf, one OpenDirAt per component,
// and returns the leaf; done releases the pinned parent.
func descend(dir *os.File, rel string) (parent *os.File, leaf string, done func(), err error) {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	parent = dir
	for _, name := range parts[:len(parts)-1] {
		next, err := OpenDirAt(parent, name)
		if parent != dir {
			parent.Close()
		}
		if err != nil {
			return nil, "", nil, err
		}
		parent = next
	}
	done = func() {
		if parent != dir {
			parent.Close()
		}
	}
	leaf = parts[len(parts)-1]
	if err := checkLeaf(leaf); err != nil {
		done()
		return nil, "", nil, err
	}
	return parent, leaf, done, nil
}

// ExistsAt reports whether anything at all sits at name directly under dir,
// without following a symlink there.
func ExistsAt(dir *os.File, name string) bool {
	if err := checkLeaf(name); err != nil {
		return false
	}
	var st unix.Stat_t
	return unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil
}

// RegularFileAt reports whether name directly under dir is a regular file,
// without following a symlink planted there.
func RegularFileAt(dir *os.File, name string) bool {
	if err := checkLeaf(name); err != nil {
		return false
	}
	var st unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false
	}
	return st.Mode&unix.S_IFMT == unix.S_IFREG
}

// ReplaceFileAt publishes data at name in one step: written to a sibling
// created exclusively for this call, then renamed over name, so a reader
// sees the old content or the new and never a partial write, and two
// replacements never share a sibling. The rename refuses nothing at name:
// a planted link or FIFO there is simply replaced.
func ReplaceFileAt(dir *os.File, name string, data []byte, perm os.FileMode) (err error) {
	if err := checkLeaf(name); err != nil {
		return err
	}
	tmp, f, err := createSiblingAt(dir, name, perm)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			unix.Unlinkat(int(dir.Fd()), tmp, 0)
		}
	}()
	if err = writeFull(f, data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return unix.Renameat(int(dir.Fd()), tmp, int(dir.Fd()), name)
}

// createSiblingAt opens a new file beside name that no other call holds:
// O_EXCL on a random suffix, tried again on a collision.
func createSiblingAt(dir *os.File, name string, perm os.FileMode) (string, *os.File, error) {
	for {
		var suffix [6]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return "", nil, err
		}
		tmp := fmt.Sprintf("%s.%x.tmp", name, suffix)
		fd, err := unix.Openat(int(dir.Fd()), tmp,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm))
		if err == nil {
			return tmp, os.NewFile(uintptr(fd), tmp), nil
		}
		if !errors.Is(err, unix.EEXIST) {
			return "", nil, err
		}
	}
}

func writeFull(f *os.File, data []byte) error {
	for len(data) > 0 {
		n, err := f.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

// MkdirAt creates directory name directly under dir.
func MkdirAt(dir *os.File, name string, perm os.FileMode) error {
	if err := checkLeaf(name); err != nil {
		return err
	}
	return unix.Mkdirat(int(dir.Fd()), name, uint32(perm))
}

// RemoveAllAt removes name under dir and, if it is a directory, everything
// inside it, entirely through *at syscalls. It never follows a symlink (at name
// or within), so a container swapping a component for a link can't redirect the
// removal outside the pinned tree. A missing name is not an error.
func RemoveAllAt(dir *os.File, name string) error {
	if err := checkLeaf(name); err != nil {
		return err
	}
	var st unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if err == unix.ENOENT {
			return nil
		}
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return unix.Unlinkat(int(dir.Fd()), name, 0)
	}
	fd, err := unix.Openat(int(dir.Fd()), name,
		unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	child := os.NewFile(uintptr(fd), name)
	names, err := child.Readdirnames(-1)
	if err != nil {
		child.Close()
		return err
	}
	for _, n := range names {
		if err := RemoveAllAt(child, n); err != nil {
			child.Close()
			return err
		}
	}
	child.Close()
	return unix.Unlinkat(int(dir.Fd()), name, unix.AT_REMOVEDIR)
}

// RemoveAt unlinks name directly under dir. A missing file is not an error.
func RemoveAt(dir *os.File, name string) error {
	if err := checkLeaf(name); err != nil {
		return err
	}
	err := unix.Unlinkat(int(dir.Fd()), name, 0)
	if err == unix.ENOENT {
		return nil
	}
	return err
}

func checkLeaf(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return fmt.Errorf("%q is not a single path element", name)
	}
	return nil
}
