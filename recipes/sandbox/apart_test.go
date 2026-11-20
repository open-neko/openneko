package main

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

func TestCloseSharedTakesOtherUsersRights(t *testing.T) {
	root := t.TempDir()
	mk := func(name string, mode os.FileMode, dir bool) string {
		p := filepath.Join(root, name)
		if dir {
			if err := os.MkdirAll(p, 0o700); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tmp := mk("tmp", 0o777|os.ModeSticky, true)
	shm := mk("dev/shm", 0o777|os.ModeSticky, true)
	open := mk("etc/open.conf", 0o666, false)
	usr := mk("usr", 0o755, true)
	runs := mk("var/lib/runs", 0o777, true)
	inside := mk("var/lib/runs/run-a", 0o777, true)
	homes := filepath.Join(root, "var/lib/homes")
	proc := mk("proc/1", 0o777, true)

	s := &Server{Workspaces: "/var/lib/runs", Homes: "/var/lib/homes"}
	closed, err := s.closeShared(root)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(closed)
	want := []string{shm, open, tmp}
	slices.Sort(want)
	if !slices.Equal(closed, want) {
		t.Fatalf("closed %v, want %v", closed, want)
	}
	for path, mode := range map[string]os.FileMode{tmp: 0o771, shm: 0o771, open: 0o660, usr: 0o755, runs: 0o711, homes: 0o711, inside: 0o777, proc: 0o777} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode() & (os.ModePerm | os.ModeSticky); got != mode {
			t.Errorf("%s: mode %v, want %v", path, got, mode)
		}
	}
}

func TestRemoveOwnIPC(t *testing.T) {
	const ipcPrivate, ipcCreat = 0, 0o1000
	shm, _, errno := syscall.Syscall(syscall.SYS_SHMGET, ipcPrivate, 4096, ipcCreat|0o600)
	if errno != 0 {
		t.Skipf("shmget: %v", errno)
	}
	msg, _, errno := syscall.Syscall(syscall.SYS_MSGGET, ipcPrivate, ipcCreat|0o600, 0)
	if errno != 0 {
		t.Fatalf("msgget: %v", errno)
	}
	sem, _, errno := syscall.Syscall(syscall.SYS_SEMGET, ipcPrivate, 1, ipcCreat|0o600)
	if errno != 0 {
		t.Fatalf("semget: %v", errno)
	}
	made := map[[2]int]bool{{0, int(shm)}: true, {1, int(msg)}: true, {2, int(sem)}: true}
	present := func() int {
		objects, err := readIPC()
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, o := range objects {
			if made[[2]int{o.table, o.id}] {
				if o.uid != os.Getuid() || o.cid != os.Getuid() {
					t.Errorf("object %v: uid %d cuid %d, want %d", o, o.uid, o.cid, os.Getuid())
				}
				n++
			}
		}
		return n
	}
	if n := present(); n != 3 {
		t.Fatalf("found %d of the 3 objects", n)
	}
	if err := removeOwnIPC(); err != nil {
		t.Fatal(err)
	}
	if n := present(); n != 0 {
		t.Fatalf("%d objects remain", n)
	}
}
