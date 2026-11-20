package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// closeShared takes write and list rights from other users on every folder
// and file of the container that the runs could share. Run users then have
// no place to write but their own run folder and home.
func (s *Server) closeShared(root string) ([]string, error) {
	in := func(dir string) string { return filepath.Join(root, strings.TrimPrefix(dir, "/")) }
	skip := map[string]bool{in("proc"): true, in("sys"): true, in(s.Workspaces): true, in(s.Homes): true}
	var closed []string
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if skip[name] {
			return filepath.SkipDir
		}
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		info, err := os.Lstat(name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		mode := info.Mode()
		if !mode.IsDir() && !mode.IsRegular() || mode&0o002 == 0 {
			return nil
		}
		next := mode.Perm() &^ 0o006
		if err := os.Chmod(name, next); err != nil {
			if errors.Is(err, syscall.EROFS) {
				return nil
			}
			return fmt.Errorf("%s: %w", name, err)
		}
		closed = append(closed, name)
		return nil
	})
	if err != nil {
		return closed, err
	}
	for _, dir := range []string{in(s.Workspaces), in(s.Homes)} {
		if err := os.MkdirAll(dir, 0o711); err != nil {
			return closed, err
		}
		if err := os.Chmod(dir, 0o711); err != nil {
			return closed, err
		}
	}
	return closed, nil
}

var ipcTables = []struct{ file, id string }{
	{"/proc/sysvipc/shm", "shmid"},
	{"/proc/sysvipc/msg", "msqid"},
	{"/proc/sysvipc/sem", "semid"},
}

type ipcObject struct {
	table    int
	id       int
	uid, cid int
}

func readIPC() ([]ipcObject, error) {
	var out []ipcObject
	for i, table := range ipcTables {
		f, err := os.Open(table.file)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(f)
		column := map[string]int{}
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(column) == 0 {
				for n, name := range fields {
					column[name] = n
				}
				continue
			}
			value := func(name string) int {
				n, ok := column[name]
				if !ok || n >= len(fields) {
					return -1
				}
				v, err := strconv.Atoi(fields[n])
				if err != nil {
					return -1
				}
				return v
			}
			out = append(out, ipcObject{table: i, id: value(table.id), uid: value("uid"), cid: value("cuid")})
		}
		err = scanner.Err()
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// removeIPC removes the System V objects that a run user owns or made. Only
// the owner, the maker or CAP_SYS_ADMIN may remove one, so a child process
// does it as that user.
func removeIPC(uid int) error {
	objects, err := readIPC()
	if err != nil {
		return err
	}
	found := false
	for _, o := range objects {
		if o.uid == uid || o.cid == uid {
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "ipc-clean")
	cmd.Env = []string{}
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(uid), Groups: []uint32{}}}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("remove the IPC objects of uid %d: %w: %s", uid, err, out)
	}
	return nil
}

func ipcCleanMain() {
	if err := removeOwnIPC(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func removeOwnIPC() error {
	objects, err := readIPC()
	if err != nil {
		return err
	}
	self := os.Getuid()
	calls := []uintptr{syscall.SYS_SHMCTL, syscall.SYS_MSGCTL, syscall.SYS_SEMCTL}
	var failed []string
	for _, o := range objects {
		if o.id < 0 || (o.uid != self && o.cid != self) {
			continue
		}
		var errno syscall.Errno
		if o.table == 2 {
			_, _, errno = syscall.Syscall6(calls[o.table], uintptr(o.id), 0, ipcRMID, 0, 0, 0)
		} else {
			_, _, errno = syscall.Syscall(calls[o.table], uintptr(o.id), ipcRMID, 0)
		}
		if errno != 0 && errno != syscall.EINVAL && errno != syscall.EIDRM {
			failed = append(failed, fmt.Sprintf("%s %d: %v", ipcTables[o.table].id, o.id, errno))
		}
	}
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	return nil
}

const ipcRMID = 0
