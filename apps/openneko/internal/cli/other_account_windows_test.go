package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

var (
	advapi32                = windows.NewLazySystemDLL("advapi32.dll")
	procLogonUser           = advapi32.NewProc("LogonUserW")
	procImpersonateLoggedOn = advapi32.NewProc("ImpersonateLoggedOnUser")
)

// otherAccount makes a local account for the test and logs it on. The
// runner of CI is an administrator, so there the test must not skip.
func otherAccount(t *testing.T) windows.Token {
	t.Helper()
	var b [5]byte
	_, _ = rand.Read(b[:])
	name := "onk" + hex.EncodeToString(b[:])
	_, _ = rand.Read(b[:])
	// net user stops at a prompt for a password longer than 14 characters.
	password := "Aa1!" + hex.EncodeToString(b[:4]) + "xY"
	if out, err := exec.Command("net", "user", name, password, "/add").CombinedOutput(); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("make a local account: %v\n%s", err, out)
		}
		t.Skipf("making a local account needs an administrator: %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("net", "user", name, "/delete").Run() })
	u, _ := windows.UTF16PtrFromString(name)
	d, _ := windows.UTF16PtrFromString(".")
	p, _ := windows.UTF16PtrFromString(password)
	var token windows.Token
	const logonNetwork, providerDefault = 3, 0
	if r, _, err := procLogonUser.Call(uintptr(unsafe.Pointer(u)), uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(p)),
		logonNetwork, providerDefault, uintptr(unsafe.Pointer(&token))); r == 0 {
		t.Fatalf("log on %s: %v", name, err)
	}
	t.Cleanup(func() { _ = token.Close() })
	return token
}

type impersonateError struct{ error }

// as runs fn on a thread that acts as the account of token. The thread is
// never handed back to the runtime, so it ends with the goroutine.
func as(t *testing.T, token windows.Token, fn func() error) error {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		if r, _, err := procImpersonateLoggedOn.Call(uintptr(token)); r == 0 {
			result <- impersonateError{err}
			return
		}
		result <- fn()
	}()
	err := <-result
	if ie := (impersonateError{}); errors.As(err, &ie) {
		t.Fatalf("act as the other account: %v", ie.error)
	}
	return err
}

func readFile(path string) func() error {
	return func() error { _, err := os.ReadFile(path); return err }
}

func dial(path string) func() error {
	return func() error {
		conn, err := net.Dial("unix", path)
		if err == nil {
			_ = conn.Close()
		}
		return err
	}
}

// openFolder gives a folder an access list that lets every account in, as
// a folder outside the profile can have.
func openFolder(t *testing.T) string {
	t.Helper()
	dir := testsock.Dir(t)
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;WD)(A;OICI;FA;;;SY)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, _ := sd.DACL()
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAnotherAccountCannotReachTheSocketsOrTheSecrets(t *testing.T) {
	other := otherAccount(t)
	open := openFolder(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	control := filepath.Join(open, "control.txt")
	if err := os.WriteFile(control, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	controlSock := filepath.Join(open, "control.sock")
	ln, err := net.Listen("unix", controlSock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	if err := as(t, other, readFile(control)); err != nil {
		t.Fatalf("control: the other account cannot read a file in an open folder: %v", err)
	}
	if err := as(t, other, dial(controlSock)); err != nil {
		t.Fatalf("control: the other account cannot connect to a socket in an open folder: %v", err)
	}

	cleanServeEnv(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(open, "c"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(open, "d"))
	if err := config.EnsureSecretKey(""); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Write(secrets.Store{"core": {"TOKEN": "x"}}, ""); err != nil {
		t.Fatal(err)
	}
	backupKey := filepath.Join(open, "b", "installation.key")
	if _, err := config.CreateBackupKey(backupKey); err != nil {
		t.Fatal(err)
	}
	layout := nativeLayout()
	if err := layout.Prepare(); err != nil {
		t.Fatal(err)
	}
	door := filepath.Join(layout.Door(), "keydoor.sock")
	if _, err := llm.ListenDoor(ctx, door, &llm.KeyDoor{}); err != nil {
		t.Fatal(err)
	}
	serveSock := filepath.Join(layout.Run(), "serve.sock")
	if _, err := serveAgentTurns(ctx, io.Discard, serveSock, 1, sandbox.Config{}, confine.State{}); err != nil {
		t.Fatal(err)
	}
	stopAdmin, err := serveWebAdmin(layout.WebAdmin(), http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	defer stopAdmin()

	for _, path := range []string{door, serveSock, layout.WebAdmin()} {
		if err := dial(path)(); err != nil {
			t.Fatalf("the owner cannot connect to %s: %v", path, err)
		}
		if err := as(t, other, dial(path)); err == nil {
			t.Errorf("the other account connects to %s", path)
		}
	}
	key := filepath.Join(config.Dir(""), "secret-key")
	for _, path := range []string{key, secrets.Path(""), backupKey} {
		if err := readFile(path)(); err != nil {
			t.Fatalf("the owner cannot read %s: %v", path, err)
		}
		if err := as(t, other, readFile(path)); !errors.Is(err, fs.ErrPermission) {
			t.Errorf("the other account reads %s: err = %v", path, err)
		}
	}
	probe := filepath.Join(layout.Files(), "probe.txt")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := as(t, other, readFile(probe)); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("the other account reads a user file: err = %v", err)
	}
}
