//go:build !windows

package llm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

// dialAs connects to the socket as the given container user. In a rootless
// Docker, user 0 is this account and any other user is another account.
func dialAs(t *testing.T, dir, user string) (string, int) {
	t.Helper()
	out, err := exec.Command("docker", "run", "--rm", "--network", "none", "-u", user,
		"-v", dir+":/door", "busybox", "/door/dial", "/door/keydoor.sock").CombinedOutput()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out)), code
}

func TestKeyDoorSocketRefusesAnotherAccount(t *testing.T) {
	dir := t.TempDir()
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

	if os.Getenv("OPENNEKO_TEST_DOCKER") == "0" {
		t.Skip("OPENNEKO_TEST_DOCKER=0: the mode check ran; the other-account dial needs Docker")
	}
	if exec.Command("docker", "image", "inspect", "busybox").Run() != nil {
		t.Skip("no Docker with the busybox image: the mode check ran; the other-account dial needs Docker")
	}
	src := filepath.Join(t.TempDir(), "dial.go")
	if err := os.WriteFile(src, []byte(doorDialer), 0o644); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(dir, "dial"), src)
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GO111MODULE=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the dialer: %v\n%s", err, out)
	}
	for _, c := range []struct {
		user string
		want int
	}{
		{"0:0", 0},
		{"4242:4242", 3},
	} {
		out, code := dialAs(t, dir, c.user)
		if code != c.want {
			t.Fatalf("container user %s: exit %d (%s), want %d", c.user, code, out, c.want)
		}
		if c.want == 3 && !strings.Contains(out, "permission denied") {
			t.Fatalf("container user %s: %s", c.user, out)
		}
		t.Logf("container user %s: %s", c.user, out)
	}
}
