package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/gjhost"
	"github.com/open-neko/neko/apps/openneko/internal/installation"
	"github.com/open-neko/neko/apps/openneko/internal/native"
)

func TestClassifyNative(t *testing.T) {
	now := time.Now()
	st := native.State{PID: 1, Started: now.Add(-time.Minute), Procs: map[string]*native.ProcState{
		"serve": {PID: os.Getpid(), Started: now.Add(-time.Minute)},
		"web":   {PID: os.Getpid(), Started: now.Add(-time.Minute), Restarts: 2},
	}}
	gj := &gjhost.State{State: gjhost.StateRunning, Since: now.Add(-time.Minute), PID: os.Getpid(), Restarts: 1}
	if state, lines := classifyNative(st, gj, now); state != serving || !strings.Contains(strings.Join(lines, "\n"), "web: pid") ||
		!strings.Contains(strings.Join(lines, "\n"), fmt.Sprintf("graphjin: pid %d, up 1m0s, 1 restarts", os.Getpid())) {
		t.Errorf("all up: %v %v", state, lines)
	}
	for _, c := range []struct {
		gj   *gjhost.State
		want string
	}{
		{nil, "graphjin: no state from serve yet"},
		{&gjhost.State{State: gjhost.StateRunning, PID: 0}, "graphjin: not running, 0 restarts"},
		{&gjhost.State{State: gjhost.StateRestarting, Reason: "graphjin exited", Restarts: 2}, "graphjin: restarting, 2 restarts; graphjin exited"},
	} {
		if state, lines := classifyNative(st, c.gj, now); state != degraded || !strings.Contains(strings.Join(lines, "\n"), c.want) {
			t.Errorf("graphjin %+v: %v %v", c.gj, state, lines)
		}
	}
	st.Procs["web"] = &native.ProcState{Restarts: 3, LastExit: "exit status 1"}
	state, lines := classifyNative(st, gj, now)
	if state != down || !strings.Contains(strings.Join(lines, "\n"), "web: not running, 3 restarts; last stop: exit status 1") {
		t.Errorf("web down: %v %v", state, lines)
	}
}

func TestNativeSettings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENNEKO_PORT", "3123")
	t.Setenv("OPENNEKO_WEB_BIND_ADDRESS", "127.0.0.1")
	t.Setenv("OPENNEKO_WEB_DIR", "")
	s, err := nativeSettings("/opt/web")
	webDir, _ := filepath.Abs("/opt/web")
	if err != nil || !s.Native() || s.Mode != "prod" || s.WebPort != 3123 || s.WebBindAddress != "127.0.0.1" || s.WebDir != webDir {
		t.Fatalf("new native settings: %+v %v", s, err)
	}
	if !strings.HasSuffix(defaultNativeWebDir(), filepath.FromSlash("/share/openneko/web")) {
		t.Errorf("default web dir %s", defaultNativeWebDir())
	}
	if err := installation.Save(config.Dir(""), installation.Settings{Version: 1, Mode: "prod", WebPort: 3000, WebBindAddress: "0.0.0.0"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := nativeInstall(); ok {
		t.Error("a Docker install is not native")
	}
	if _, err := nativeSettings(""); err == nil || !strings.Contains(err.Error(), "runs on Docker") {
		t.Errorf("a native start on a Docker install: %v", err)
	}
}
