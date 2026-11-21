//go:build linux || darwin

package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/native"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
	"github.com/open-neko/neko/apps/openneko/internal/testsock"
)

// notForRealStart: a sandbox URL makes start check the sandbox, and
// GJ_DATABASE_PASSWORD gives the customer GraphJin a database block that
// stops it. TestNoOpenNekoProcessShowsAPlantedSecret covers both.
var notForRealStart = map[string]bool{"OPENNEKO_SANDBOX_URL": true, "GJ_DATABASE_PASSWORD": true}

func buildOpenNeko(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "openneko")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/openneko")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build openneko: %v\n%s", err, out)
	}
	return bin
}

// plantedDatabase makes a database with a login of its own, whose password
// appears nowhere else.
func plantedDatabase(t *testing.T) (string, string) {
	t.Helper()
	admin := testpg.URL(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	password := "pw" + hex.EncodeToString(raw)
	name := fmt.Sprintf("ontest_%d_%d_secrets", time.Now().Unix(), os.Getpid())
	role := name + "_login"
	for _, sql := range []string{
		"create role " + role + " login superuser password '" + password + "'",
		"create database " + name + " owner " + role,
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), admin)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), "drop database if exists "+name+" with (force)")
		_, _ = c.Exec(context.Background(), "drop role if exists "+role)
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, password)
	u.Path = "/" + name
	return u.String(), password
}

// openNekoProcs lists the processes of bin below root, with root.
func openNekoProcs(t *testing.T, bin string, root int) map[int]string {
	t.Helper()
	raw, err := exec.Command("ps", "axo", "pid=,ppid=,args=").Output()
	if err != nil {
		t.Fatal(err)
	}
	parent, args := map[int]int{}, map[int]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		ppid, _ := strconv.Atoi(f[1])
		parent[pid], args[pid] = ppid, strings.Join(f[2:], " ")
	}
	out := map[int]string{}
	for pid := range args {
		for p, depth := pid, 0; p > 0 && depth < 16; p, depth = parent[p], depth+1 {
			if p == root {
				if strings.HasPrefix(args[pid], bin+" ") {
					out[pid] = strings.TrimPrefix(args[pid], bin+" ")
				}
				break
			}
		}
	}
	return out
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestTheRealStartShowsNoPlantedSecret runs the release executable: `openneko
// start --native`, in the foreground and with --detach, with secrets in its
// shell and the database URL in its arguments. No process of the chain may
// show one of them in its arguments or environment.
func TestTheRealStartShowsNoPlantedSecret(t *testing.T) {
	dbURL, password := plantedDatabase(t)
	for _, addr := range []string{native.ServeAddr, native.GraphJinAddr, native.SubscriptionsAddr} {
		if ln, err := net.Listen("tcp", addr); err != nil {
			t.Fatalf("this test needs %s free for the native install: %v", addr, err)
		} else {
			ln.Close()
		}
	}
	bin := buildOpenNeko(t)
	planted := []string{dbURL, password}
	for name, v := range plantedSecrets {
		if !notForRealStart[name] {
			planted = append(planted, v)
		}
	}
	for _, detach := range []bool{true, false} {
		t.Run(fmt.Sprintf("detach=%v", detach), func(t *testing.T) {
			home := filepath.Join(testsock.Dir(t), "h")
			web := filepath.Join(home, "web")
			if err := os.MkdirAll(web, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html>"), 0o600); err != nil {
				t.Fatal(err)
			}
			port := freePort(t)
			env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + os.TempDir(),
				"XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "XDG_DATA_HOME=" + filepath.Join(home, "data"),
				"XDG_STATE_HOME=" + filepath.Join(home, "state"), "OPENNEKO_PORT=" + strconv.Itoa(port),
				"OPENNEKO_WEB_BIND_ADDRESS=127.0.0.1"}
			repo, _ := filepath.Abs(filepath.Join("..", "..", "..", ".."))
			env = append(env, "OPENNEKO_BUILTIN_SKILLS_ROOT="+filepath.Join(repo, "packages", "llm", "assets", "builtin-skills"),
				"OPENNEKO_SANDBOX_SKILLS_ROOT="+filepath.Join(repo, "recipes", "sandbox", "skills"), "OPENNEKO_PACKS_DIR="+filepath.Join(repo, "packs"))
			for name, v := range plantedSecrets {
				if !notForRealStart[name] {
					env = append(env, name+"="+v)
				}
			}
			args := []string{"start", "--native", "--web-dir", web, "--database-url", dbURL}
			var out bytes.Buffer
			t.Cleanup(func() {
				if t.Failed() {
					for _, name := range []string{"logs/web.log", "agent/logs/serve.log", "logs/serve.log", "logs/graphjin.log"} {
						raw, _ := os.ReadFile(filepath.Join(home, "data", "openneko", name))
						t.Logf("%s:\n%s", name, raw[max(0, len(raw)-3000):])
					}
				}
			})
			stop := func() {
				c := exec.Command(bin, "stop")
				c.Env = env
				_ = c.Run()
			}
			t.Cleanup(stop)
			var root int
			if detach {
				c := exec.Command(bin, append(args, "-d")...)
				c.Env, c.Stdout, c.Stderr = env, &out, &out
				if err := c.Run(); err != nil {
					t.Fatalf("start -d: %v\n%s", err, out.String())
				}
				for pid, a := range openNekoProcs(t, bin, 1) {
					if a == "native-run" {
						root = pid
					}
				}
				if root == 0 {
					t.Fatalf("no supervisor after start -d\n%s", out.String())
				}
			} else {
				c := exec.Command(bin, args...)
				c.Env, c.Stdout, c.Stderr = env, &out, &out
				c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				if err := c.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan struct{})
				go func() { _ = c.Wait(); close(done) }()
				t.Cleanup(func() {
					_ = c.Process.Signal(os.Interrupt)
					select {
					case <-done:
					case <-time.After(30 * time.Second):
						_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
						<-done
					}
				})
				deadline := time.Now().Add(3 * time.Minute)
				for {
					res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/version", port))
					if err == nil {
						res.Body.Close()
						break
					}
					select {
					case <-done:
						t.Fatalf("start ended: %s", out.String())
					default:
					}
					if time.Now().After(deadline) {
						t.Fatalf("web did not answer: %v\n%s", err, out.String())
					}
					time.Sleep(200 * time.Millisecond)
				}
				root = c.Process.Pid
			}
			for wait := time.Now().Add(90 * time.Second); ; {
				status := exec.Command(bin, "status")
				status.Env = env
				raw, err := status.CombinedOutput()
				if err == nil {
					break
				}
				if time.Now().After(wait) {
					t.Fatalf("status: %v\n%s\n%s", err, raw, out.String())
				}
				time.Sleep(time.Second)
			}
			var procs map[int]string
			deadline := time.Now().Add(30 * time.Second)
			for {
				procs = openNekoProcs(t, bin, root)
				roles := map[string]bool{}
				for _, a := range procs {
					roles[strings.Fields(a + " x")[0]] = true
				}
				if roles["web"] && roles["serve"] && roles["serve-network"] || time.Now().After(deadline) {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
			t.Logf("processes: %v", procs)
			if len(procs) < 4 {
				t.Fatalf("want the supervisor, web, serve and its network part; got %v\n%s", procs, out.String())
			}
			for pid, role := range procs {
				block, err := processBlock(pid)
				if err != nil {
					t.Errorf("read %s (pid %d): %v", role, pid, err)
					continue
				}
				for _, v := range planted {
					if bytes.Contains(block, []byte(v)) {
						t.Errorf("%s (pid %d) shows %q in its arguments or environment", role, pid, v)
					}
				}
			}
		})
	}
}
