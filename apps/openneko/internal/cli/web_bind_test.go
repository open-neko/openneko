package cli

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/compose"
)

func TestWebListenAddrKeepsLoopbackUntilSignIn(t *testing.T) {
	for _, c := range []struct {
		addr                      string
		fromFlag, live, container bool
		want                      string
		refused                   bool
	}{
		{addr: "0.0.0.0:3000", want: "127.0.0.1:3000"},
		{addr: ":3000", want: "127.0.0.1:3000"},
		{addr: "[::]:3000", want: "127.0.0.1:3000"},
		{addr: "192.0.2.10:3000", want: "127.0.0.1:3000"},
		{addr: "0.0.0.0:3000", fromFlag: true, refused: true},
		{addr: "127.0.0.1:3000", fromFlag: true, want: "127.0.0.1:3000"},
		{addr: "[::1]:3000", want: "[::1]:3000"},
		{addr: "localhost:3000", fromFlag: true, want: "localhost:3000"},
		{addr: "0.0.0.0:3000", live: true, want: "0.0.0.0:3000"},
		{addr: "0.0.0.0:3000", fromFlag: true, live: true, want: "0.0.0.0:3000"},
		{addr: ":8080", container: true, want: ":8080"},
	} {
		got, note, err := webListenAddr(c.addr, c.fromFlag, c.live, c.container)
		if c.refused {
			if err == nil || !strings.Contains(err.Error(), "sign-in is not set up") || strings.Contains(err.Error(), "\n") {
				t.Errorf("%+v: err %v, want one line that names sign-in", c, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%+v: got %q, %v; want %q", c, got, err, c.want)
		}
		if (got != c.addr) != (note != "") {
			t.Errorf("%+v: note %q", c, note)
		}
	}
}

func TestDockerStartPublishesWebOnLoopbackUntilSignIn(t *testing.T) {
	old := dockerSignInState
	t.Cleanup(func() { dockerSignInState = old })
	for _, c := range []struct {
		bind, state string
		probeErr    error
		want        string
		asked       bool
	}{
		{bind: "", state: "off", want: "127.0.0.1", asked: true},
		{bind: "0.0.0.0", state: "", want: "127.0.0.1", asked: true},
		{bind: "0.0.0.0", state: "on", want: "0.0.0.0", asked: true},
		{bind: "127.0.0.1", state: "off", want: "127.0.0.1"},
		{bind: "0.0.0.0", probeErr: errors.New("no docker"), want: "0.0.0.0", asked: true},
	} {
		t.Setenv("OPENNEKO_WEB_BIND_ADDRESS", c.bind)
		asked := false
		dockerSignInState = func(context.Context, *compose.Supervisor, string, []string, []string) (string, error) {
			asked = true
			return c.state, c.probeErr
		}
		var out strings.Builder
		restore, err := publishWebForSignIn(t.Context(), &out, nil, "p", nil, nil)
		if (err != nil) != (c.probeErr != nil) {
			t.Fatalf("%+v: err %v", c, err)
		}
		got := os.Getenv("OPENNEKO_WEB_BIND_ADDRESS")
		if got == "" {
			got = "0.0.0.0"
		}
		if got != c.want || asked != c.asked {
			t.Errorf("%+v: publish on %q, asked %v", c, got, asked)
		}
		if (c.want == "127.0.0.1" && c.bind != "127.0.0.1") != strings.Contains(out.String(), "Sign-in is not set up") {
			t.Errorf("%+v: output %q", c, out.String())
		}
		restore()
		if now := os.Getenv("OPENNEKO_WEB_BIND_ADDRESS"); now != c.bind {
			t.Errorf("%+v: after start the variable is %q", c, now)
		}
	}
}

func hostAddress(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			return n.IP.String()
		}
	}
	t.Fatal("this host has no address other than loopback")
	return ""
}

func startWebAt(t *testing.T, ctx context.Context, flagAddr string) <-chan error {
	t.Helper()
	app := t.TempDir()
	if err := os.WriteFile(filepath.Join(app, "index.html"), []byte("<html>app</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENNEKO_DATABASE_URL_FILE", "")
	t.Setenv("OPENNEKO_IN_CONTAINER", "false")
	checkoutAssets(t)
	cmd := newWebCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	args := []string{"--dir", app, "--jobs=false"}
	if flagAddr != "" {
		args = append(args, "--addr", flagAddr)
	}
	cmd.SetArgs(args)
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	return done
}

func TestWebWithoutSignInListensOnLoopbackOnly(t *testing.T) {
	host := hostAddress(t)
	_, port, _ := net.SplitHostPort(freeAddr(t))
	t.Setenv("OPENNEKO_WEB_ADDR", net.JoinHostPort("0.0.0.0", port))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := startWebAt(t, ctx, "")
	deadline := time.Now().Add(10 * time.Second)
	for {
		res, err := http.Get("http://" + net.JoinHostPort("127.0.0.1", port) + "/api/v1/version")
		if err == nil {
			_ = res.Body.Close()
			break
		}
		select {
		case err := <-done:
			t.Fatalf("web stopped: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("web did not answer on loopback: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), time.Second); err == nil {
		_ = conn.Close()
		t.Fatalf("web with no sign-in answers on %s", host)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("web did not stop")
	}

	err := <-startWebAt(t, context.Background(), net.JoinHostPort("0.0.0.0", port))
	if err == nil || !strings.Contains(err.Error(), "sign-in is not set up") {
		t.Fatalf("web --addr 0.0.0.0 with no sign-in: %v", err)
	}
}
