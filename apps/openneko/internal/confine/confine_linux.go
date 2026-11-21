package confine

import (
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	fsResolveUnix = 1 << 16
	restrictTSync = 1 << 3
)

const (
	fileRights = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_TRUNCATE | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	readRights  = unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR
	writeRights = readRights | unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE | unix.LANDLOCK_ACCESS_FS_MAKE_DIR | unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_MAKE_FIFO | unix.LANDLOCK_ACCESS_FS_MAKE_SYM |
		unix.LANDLOCK_ACCESS_FS_REFER | unix.LANDLOCK_ACCESS_FS_TRUNCATE
	execRights = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_READ_FILE
)

// handled lists the file rights that each Landlock ABI can refuse.
func handled(abi int) uint64 {
	fs := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_DIR | unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_MAKE_FIFO | unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM)
	if abi >= 2 {
		fs |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		fs |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		fs |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	if abi >= 9 {
		fs |= fsResolveUnix
	}
	return fs
}

// gaps names what a Landlock ABI below the newest one cannot refuse.
func gaps(abi int) []string {
	var out []string
	if abi < 2 {
		out = append(out, "a rename or link between folders fails (ABI 2)")
	}
	if abi < 3 {
		out = append(out, "truncate of a file outside its folders (ABI 3)")
	}
	if abi < 5 {
		out = append(out, "ioctl on a device (ABI 5)")
	}
	if abi < 6 {
		out = append(out, "signals to other processes and their abstract sockets (ABI 6)")
	}
	if abi < 9 {
		out = append(out, "connect to a named socket outside its list (ABI 9)")
	}
	return out
}

func kernel() string {
	var u unix.Utsname
	if unix.Uname(&u) != nil {
		return "unknown"
	}
	return unix.ByteSliceToString(u.Release[:])
}

// abiCap lets a test act as an older kernel.
var abiCap int

func abiOf() (int, syscall.Errno) {
	r, _, e := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if e != 0 {
		return 0, e
	}
	if abiCap > 0 && int(r) > abiCap {
		return abiCap, 0
	}
	return int(r), 0
}

// Stacks is true: Landlock domains stack, so a confined serve may start an
// agent runner that confines itself further.
const Stacks = true

// Available tells what this kernel can give, with no change to the process.
func Available() State {
	abi, e := abiOf()
	if abi < 1 {
		return State{Mechanism: "landlock", Reason: noLandlock(e, kernel(), InContainer())}
	}
	st := State{Mechanism: "landlock", ABI: abi, Confined: true, Gaps: gaps(abi)}
	if bus := SessionBus(); bus != "" && abi < 9 {
		st.Gaps = append(st.Gaps, "connect to the session bus "+bus+", through which systemd --user can start a program outside the confinement (ABI 9)")
	}
	return st
}

// SessionBus is the socket of the D-Bus session bus of this login, or "".
// A system account with no login session has none.
func SessionBus() string {
	path := ""
	if addr, ok := strings.CutPrefix(os.Getenv("DBUS_SESSION_BUS_ADDRESS"), "unix:path="); ok {
		path, _, _ = strings.Cut(addr, ",")
	} else {
		dir := os.Getenv("XDG_RUNTIME_DIR")
		if dir == "" {
			dir = fmt.Sprintf("/run/user/%d", os.Getuid())
		}
		path = filepath.Join(dir, "bus")
	}
	if info, err := os.Stat(path); err != nil || info.Mode()&os.ModeSocket == 0 {
		return ""
	}
	return path
}

// coversBus returns the folder of spec that would let a connect reach bus.
func coversBus(sockDirs []string, bus string) string {
	for _, dir := range sockDirs {
		if rel, err := filepath.Rel(dir, bus); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return dir
		}
	}
	return ""
}

// noLandlock tells a kernel without Landlock apart from a kernel that has it
// off and from a container engine that refuses the call.
func noLandlock(e syscall.Errno, release string, container bool) string {
	switch {
	case e == unix.EOPNOTSUPP:
		return fmt.Sprintf("Linux %s has Landlock but it is off: add landlock to the lsm= boot list", release)
	case container && kernelAtLeast(release, 5, 13):
		return fmt.Sprintf("the container engine blocks Landlock (Linux %s has it): use an engine whose default seccomp profile allows the landlock calls (INSTALL.md, \"Agent confinement\")", release)
	}
	return fmt.Sprintf("Linux %s has no Landlock: use kernel 5.13 or later with landlock in the lsm= boot list", release)
}

func kernelAtLeast(release string, major, minor int) bool {
	var a, b int
	if n, _ := fmt.Sscanf(release, "%d.%d", &a, &b); n < 2 {
		return false
	}
	return a > major || a == major && b >= minor
}

// InContainer tells whether this process runs in a Docker, Podman or other
// OCI container.
func InContainer() bool {
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return os.Getenv("container") != ""
}

// LSMList reads the active Linux security modules.
func LSMList() (string, error) {
	b, err := os.ReadFile("/sys/kernel/security/lsm")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// Kernel names the running kernel for a message.
func Kernel() string { return "Linux " + kernel() }

// Apply confines the process with spec. It needs a build with CGO_ENABLED=0
// below ABI 8, because Landlock binds per thread.
func Apply(spec Spec) (State, error) {
	st := Available()
	if !st.Confined {
		return st, fmt.Errorf("%w: %s", ErrUnsupported, st.Reason)
	}
	fs := handled(st.ABI)
	attr := unix.LandlockRulesetAttr{Access_fs: fs}
	if spec.NoTCP && st.ABI >= 4 {
		attr.Access_net = unix.LANDLOCK_ACCESS_NET_BIND_TCP | unix.LANDLOCK_ACCESS_NET_CONNECT_TCP
	}
	size := unsafe.Sizeof(attr)
	switch {
	case st.ABI >= 6:
		attr.Scoped = unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET | unix.LANDLOCK_SCOPE_SIGNAL
	case st.ABI >= 4:
		size = 16
	default:
		size = 8
	}
	r, _, e := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), size, 0)
	if e != 0 {
		return fail(st, "create the Landlock ruleset", e)
	}
	rs := int(r)
	defer unix.Close(rs)
	add := func(paths []string, rights uint64) error {
		for _, p := range paths {
			if err := addPath(rs, p, rights&fs); err != nil {
				return err
			}
		}
		return nil
	}
	sockDirs := make([]string, 0, len(spec.Sockets)+len(spec.Listen))
	listenDirs := make([]string, 0, len(spec.Listen))
	for _, s := range spec.Sockets {
		sockDirs = append(sockDirs, filepath.Dir(s))
	}
	for _, s := range spec.Listen {
		sockDirs = append(sockDirs, filepath.Dir(s))
		listenDirs = append(listenDirs, filepath.Dir(s))
	}
	if bus := SessionBus(); bus != "" && st.ABI >= 9 {
		if dir := coversBus(sockDirs, bus); dir != "" {
			return fail(st, "keep the session bus "+bus+" out of reach: move the sockets in "+dir+" to a folder of their own", unix.EINVAL)
		}
	}
	for _, step := range []struct {
		paths  []string
		rights uint64
	}{{spec.Read, readRights}, {spec.ReadWrite, writeRights}, {listenDirs, writeRights}, {spec.Exec, execRights}, {sockDirs, fsResolveUnix}} {
		if err := add(step.paths, step.rights); err != nil {
			st.Confined = false
			st.Reason = err.Error()
			return st, err
		}
	}
	if st.ABI >= 8 {
		return restrictAll(st, rs)
	}
	if _, _, e := syscall.AllThreadsSyscall(syscall.SYS_PRCTL, unix.PR_SET_NO_NEW_PRIVS, 1, 0); e != 0 {
		return fail(st, "set no_new_privs on every thread", e)
	}
	if _, _, e := syscall.AllThreadsSyscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(rs), 0, 0); e != 0 {
		return fail(st, "apply the Landlock ruleset to every thread", e)
	}
	return st, nil
}

// restrictAll uses TSYNC, which also gives every thread no_new_privs, so it
// works in a cgo build too.
func restrictAll(st State, rs int) (State, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fail(st, "set no_new_privs", err.(syscall.Errno))
	}
	if _, _, e := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(rs), restrictTSync, 0); e != 0 {
		return fail(st, "apply the Landlock ruleset to every thread", e)
	}
	return st, nil
}

func fail(st State, what string, e syscall.Errno) (State, error) {
	err := fmt.Errorf("%s: %w", what, e)
	if errors.Is(e, syscall.ENOTSUP) {
		err = fmt.Errorf("%s: this build uses cgo; build openneko with CGO_ENABLED=0: %w", what, e)
	}
	st.Confined, st.Reason = false, err.Error()
	return st, err
}

// addPath skips a path that does not exist: a rule there would give nothing.
func addPath(rs int, path string, rights uint64) error {
	if path == "" || rights == 0 {
		return nil
	}
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s for a Landlock rule: %w", path, err)
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		rights &= fileRights
		if rights == 0 {
			return nil
		}
	}
	attr := unix.LandlockPathBeneathAttr{Allowed_access: rights, Parent_fd: int32(fd)}
	if _, _, e := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(rs), unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&attr)), 0, 0, 0); e != 0 {
		return fmt.Errorf("add a Landlock rule for %s: %w", path, e)
	}
	return nil
}

// Program lists what running path needs: the file and, for a dynamic ELF
// file, its loader to run and the library folders to read.
func Program(path string) (exec, read []string) {
	exec = []string{path}
	f, err := elf.Open(path)
	if err != nil {
		return exec, nil
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type != elf.PT_INTERP {
			continue
		}
		b := make([]byte, p.Filesz)
		if _, err := p.ReadAt(b, 0); err != nil {
			return exec, nil
		}
		loader := strings.TrimRight(string(b), "\x00")
		if real, err := filepath.EvalSymlinks(loader); err == nil {
			exec = append(exec, real)
		}
		exec = append(exec, loader)
		read = []string{"/etc/ld.so.cache", "/lib", "/lib64", "/usr/lib", "/usr/lib64", "/usr/local/lib"}
	}
	return exec, read
}
