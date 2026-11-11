// Package gjhost runs the GraphJin engines of openneko serve.
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
	StateFile      = ".openneko-graphjin-state"
	staleAgentKey  = ".openneko-graphjin-agent-key"
)

type instance struct {
	gj   *serv.HttpService
	srv  *http.Server
	done chan error
}

// Serve runs one GraphJin engine on addr ("unix:/path" or host:port) until
// ctx ends or the engine stops. GraphJin's own Fatal calls end the process.
func Serve(ctx context.Context, configFile, addr string, log io.Writer) error {
	stopTelemetry, err := startTelemetry(ctx)
	if err != nil {
		return err
	}
	defer func() {
		flush, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		stopTelemetry(flush)
	}()
	in, err := start(configFile, addr, log)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		stop(in, time.Second)
		return nil
	case err := <-in.done:
		stop(in, time.Second)
		if err == nil {
			err = errors.New("graphjin stopped")
		}
		return err
	}
}

func listen(addr string) (net.Listener, error) {
	if path, ok := strings.CutPrefix(addr, "unix:"); ok {
		_ = os.Remove(path)
		return net.Listen("unix", path)
	}
	return net.Listen("tcp", addr)
}

func start(configFile, addr string, log io.Writer) (*instance, error) {
	name := strings.TrimSuffix(configFile, filepath.Ext(configFile))
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
	ln, err := listen(addr)
	if err != nil {
		_ = gj.Close()
		return nil, err
	}
	// The write timeout matches upstream's server; config mutations extend
	// their own deadline per request.
	srv := &http.Server{
		Handler:           withTraceContext(mux),
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
	if log != nil {
		fmt.Fprintf(log, "graphjin listening on %s (%s)\n", ln.Addr(), configFile)
	}
	return in, nil
}

func stop(in *instance, drain time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	_ = in.srv.Shutdown(ctx)
	_ = in.gj.Close()
}

func readToken(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writeAtomic(path string, data []byte) error {
	tmp := fmt.Sprintf("%s.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
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
