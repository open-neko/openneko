//go:build !windows

package llm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/testsock"
)

const doorDialer = `package main

import (
	"fmt"
	"net"
	"os"
)

func main() {
	conn, err := net.Dial("unix", os.Args[1])
	if err != nil {
		fmt.Println("refused:", err)
		os.Exit(3)
	}
	conn.Close()
	fmt.Println("connected")
}
`

func exitCode(t *testing.T, out []byte, err error) (string, int) {
	t.Helper()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out)), code
}

func buildDialer(t *testing.T, out string, env ...string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "dial.go")
	if err := os.WriteFile(src, []byte(doorDialer), 0o644); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", out, src)
	build.Env = append(os.Environ(), append([]string{"CGO_ENABLED=0", "GO111MODULE=off"}, env...)...)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the dialer: %v\n%s", err, out)
	}
	if err := os.Chmod(out, 0o755); err != nil {
		t.Fatal(err)
	}
}

// dockerDialer connects to the socket as a container user. In a rootless
// Docker, user 0 is this account and any other user is another account.
func dockerDialer(t *testing.T, dir string) func(other bool) (string, int) {
	buildDialer(t, filepath.Join(dir, "dial"), "GOOS=linux")
	return func(other bool) (string, int) {
		user := "0:0"
		if other {
			user = "4242:4242"
		}
		out, err := exec.Command("docker", "run", "--rm", "--network", "none", "-u", user,
			"-v", dir+":/door", "busybox", "/door/dial", "/door/keydoor.sock").CombinedOutput()
		return exitCode(t, out, err)
	}
}

// sudoDialer connects as this account and as nobody. Only CI sets
// OPENNEKO_TEST_OTHER_ACCOUNT=sudo; its runners allow sudo with no password.
func sudoDialer(t *testing.T, dir string) func(other bool) (string, int) {
	dial, sock := filepath.Join(dir, "dial"), filepath.Join(dir, "keydoor.sock")
	buildDialer(t, dial)
	return func(other bool) (string, int) {
		cmd := exec.Command(dial, sock)
		if other {
			cmd = exec.Command("sudo", "-n", "-u", "nobody", dial, sock)
		}
		out, err := cmd.CombinedOutput()
		return exitCode(t, out, err)
	}
}

func TestKeyDoorSocketRefusesAnotherAccount(t *testing.T) {
	dir := testsock.Dir(t)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "keydoor.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := ListenDoor(ctx, path, &KeyDoor{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o660 {
		t.Fatalf("the door socket has mode %o, want 660: only its owner and serve's group may connect", perm)
	}

	var dialAs func(other bool) (string, int)
	switch {
	case os.Getenv("OPENNEKO_TEST_OTHER_ACCOUNT") == "sudo":
		dialAs = sudoDialer(t, dir)
	case os.Getenv("OPENNEKO_TEST_DOCKER") != "0" && exec.Command("docker", "image", "inspect", "busybox").Run() == nil:
		dialAs = dockerDialer(t, dir)
	case os.Getenv("CI") != "":
		t.Fatal("CI must test the door with another account: set OPENNEKO_TEST_OTHER_ACCOUNT=sudo")
	default:
		t.Skip("no Docker with the busybox image and no OPENNEKO_TEST_OTHER_ACCOUNT=sudo: the mode check ran; the other-account dial did not")
	}
	for _, other := range []bool{false, true} {
		out, code := dialAs(other)
		want := 0
		if other {
			want = 3
		}
		if code != want {
			t.Fatalf("other account %v: exit %d (%s), want %d", other, code, out, want)
		}
		if other && !strings.Contains(out, "permission denied") {
			t.Fatalf("other account: %s", out)
		}
		t.Logf("other account %v: %s", other, out)
	}
}
