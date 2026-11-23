package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var (
	ownWalkHook       func(dir string)
	openNoFollowFirst = true
)

// openBeneath opens the folder at path below base. The operator chose base
// and root owns its parents, so a link there is followed; the user owns
// everything below base, so no link below it is.
func openBeneath(base, path string) (int, error) {
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return -1, fmt.Errorf("%s is not below %s", path, base)
	}
	bfd, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open %s: %w", base, err)
	}
	if rel == "." {
		return bfd, nil
	}
	defer unix.Close(bfd)
	fd, err := -1, error(unix.ENOSYS)
	if openNoFollowFirst {
		how := unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_BENEATH}
		fd, err = unix.Openat2(bfd, rel, &how)
	}
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) {
		fd, err = openEachNoFollow(bfd, rel)
	}
	if err != nil {
		return -1, fmt.Errorf("open %s: %w", path, err)
	}
	return fd, nil
}

func openEachNoFollow(dirfd int, rel string) (int, error) {
	fd, err := unix.Dup(dirfd)
	if err != nil {
		return -1, err
	}
	for _, name := range strings.Split(rel, "/") {
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	return fd, nil
}

// ownDir gives the folder at path below base to uid:gid with the mode.
func ownDir(base, path string, uid, gid int, mode os.FileMode) error {
	fd, err := openBeneath(base, path)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err := unix.Fchown(fd, uid, gid); err != nil {
		return fmt.Errorf("chown %s: %w", path, err)
	}
	if err := unix.Fchmod(fd, uint32(mode.Perm())); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// ownTreeNoFollow gives every entry below root to uid:gid and drops the
// setgid bit of folders. It never follows a link and never enters a folder
// in skip.
func ownTreeNoFollow(root string, uid, gid int, skip map[string]bool) error {
	fd, err := openBeneath(root, root)
	if err != nil {
		return err
	}
	return ownAt(fd, root, uid, gid, skip)
}

func ownAt(dirfd int, path string, uid, gid int, skip map[string]bool) error {
	dir := os.NewFile(uintptr(dirfd), path)
	defer dir.Close()
	var st unix.Stat_t
	if err := unix.Fstat(dirfd, &st); err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if err := unix.Fchown(dirfd, uid, gid); err != nil {
		return fmt.Errorf("chown %s: %w", path, err)
	}
	if st.Mode&unix.S_ISGID != 0 {
		if err := unix.Fchmod(dirfd, st.Mode&0o777); err != nil {
			return fmt.Errorf("chmod %s: %w", path, err)
		}
	}
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if ownWalkHook != nil {
		ownWalkHook(path)
	}
	for _, name := range names {
		p := filepath.Join(path, name)
		if skip[p] {
			continue
		}
		child, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		switch {
		case err == nil:
			if err := ownAt(child, p, uid, gid, skip); err != nil {
				return err
			}
			continue
		case errors.Is(err, unix.ENOENT):
			continue
		case !errors.Is(err, unix.ENOTDIR) && !errors.Is(err, unix.ELOOP):
			return fmt.Errorf("open %s: %w", p, err)
		}
		if err := unix.Fchownat(dirfd, name, uid, gid, unix.AT_SYMLINK_NOFOLLOW); err != nil && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("chown %s: %w", p, err)
		}
	}
	return nil
}
