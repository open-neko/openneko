// Package gjhost runs the customer GraphJin inside openneko serve.
package gjhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/dosco/graphjin/serv/v3"
)

const (
	RestartFile    = ".openneko-graphjin-restart"
	RestartAckFile = ".openneko-graphjin-restart-ack"
	PingFile       = ".openneko-graphjin-supervisor-ping"
	PingAckFile    = ".openneko-graphjin-supervisor-ping-ack"
	staleAgentKey  = ".openneko-graphjin-agent-key"
)

type Host struct {
	ConfigFile string
	Addr       string
	Log        io.Writer
	Poll       time.Duration
}

type instance struct {
	gj   *serv.HttpService
	srv  *http.Server
	done chan error
}

// Run serves GraphJin until ctx ends. A new token in RestartFile replaces the
// running engine with one built from the config on disk, as the old
// supervisor script did.
func (h *Host) Run(ctx context.Context) error {
	dir := filepath.Dir(h.ConfigFile)
	poll := h.Poll
	if poll == 0 {
		poll = time.Second
	}
	_ = os.Remove(filepath.Join(dir, staleAgentKey))
	last := readToken(filepath.Join(dir, RestartAckFile))

	cur, err := h.start()
	if err != nil {
		h.logf("graphjin did not start: %v", err)
	}
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		var done chan error
		if cur != nil {
			done = cur.done
		}
		select {
		case <-ctx.Done():
			h.stop(cur, time.Second)
			return nil
		case err := <-done:
			h.logf("graphjin stopped: %v", err)
			h.stop(cur, time.Second)
			cur = nil
		case <-tick.C:
		}
		if token := readToken(filepath.Join(dir, RestartFile)); token != "" && token != last {
			h.stop(cur, 5*time.Second)
			if cur, err = h.start(); err != nil {
				h.logf("graphjin did not start: %v", err)
			}
			last = token
			if err := writeAtomic(filepath.Join(dir, RestartAckFile), token); err != nil {
				h.logf("graphjin restart ack: %v", err)
			}
		} else if cur == nil {
			if cur, err = h.start(); err != nil {
				h.logf("graphjin did not start: %v", err)
			}
		}
		if token := readToken(filepath.Join(dir, PingFile)); token != "" {
			if err := writeAtomic(filepath.Join(dir, PingAckFile), token); err != nil {
				h.logf("graphjin ping ack: %v", err)
			}
		}
	}
}

func (h *Host) start() (*instance, error) {
	name := strings.TrimSuffix(h.ConfigFile, filepath.Ext(h.ConfigFile))
	conf, err := serv.ReadInConfig(name)
	if err != nil {
		return nil, err
	}
	gj, err := serv.NewGraphJinService(conf)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	if err := gj.Attach(mux); err != nil {
		_ = gj.Close()
		return nil, err
	}
	ln, err := net.Listen("tcp", h.Addr)
	if err != nil {
		_ = gj.Close()
		return nil, err
	}
	// The write timeout matches upstream's server; config mutations extend
	// their own deadline per request.
	srv := &http.Server{
		Handler:           mux,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	in := &instance{gj: gj, srv: srv, done: make(chan error, 1)}
	go func() {
		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		in.done <- err
	}()
	h.logf("graphjin listening on %s (%s)", ln.Addr(), h.ConfigFile)
	return in, nil
}

func (h *Host) stop(in *instance, drain time.Duration) {
	if in == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	_ = in.srv.Shutdown(ctx)
	_ = in.gj.Close()
}

func (h *Host) logf(format string, args ...any) {
	if h.Log != nil {
		fmt.Fprintf(h.Log, format+"\n", args...)
	}
}

func readToken(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writeAtomic(path, token string) error {
	tmp := fmt.Sprintf("%s.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, []byte(token), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Version reports the serv/v3 module version linked into the binary.
func Version() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/dosco/graphjin/serv/v3" {
				return strings.TrimPrefix(dep.Version, "v")
			}
		}
	}
	return "unknown"
}
