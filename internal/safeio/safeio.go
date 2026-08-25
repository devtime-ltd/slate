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
	"fmt"
	"io"
	"os"
	"path/filepath"

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
