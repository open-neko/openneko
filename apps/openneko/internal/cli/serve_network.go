package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/neko/apps/openneko/internal/db"
	"github.com/open-neko/neko/apps/openneko/internal/filecontent"
	"github.com/open-neko/neko/apps/openneko/internal/files"
	"github.com/open-neko/neko/apps/openneko/internal/filestore"
	"github.com/open-neko/neko/apps/openneko/internal/gjhost"
	"github.com/open-neko/neko/apps/openneko/internal/native"
	"github.com/open-neko/neko/apps/openneko/internal/webfront"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

const serveNetworkCmd = "serve-network"

// networkStart is what the network part of serve gets on its stdin at start.
// It reads no credentials file, so its confinement can hide them all.
type networkStart struct {
	TokenKey            []byte `json:"token_key"`
	DatabaseURL         string `json:"database_url"`
	Addr                string `json:"addr"`
	FilesDir            string `json:"files_dir"`
	IndexDir            string `json:"index_dir"`
	DuckDB              string `json:"duckdb"`
	CheckSocket         string `json:"check_socket,omitempty"`
	GraphJinConfig      string `json:"graphjin_config,omitempty"`
	GraphJinAddr        string `json:"graphjin_addr,omitempty"`
	GraphJinSocket      string `json:"graphjin_socket,omitempty"`
	SubscriptionsAddr   string `json:"subscriptions_addr,omitempty"`
	SubscriptionsDir    string `json:"subscriptions_dir,omitempty"`
	SubscriptionsURL    string `json:"subscriptions_url,omitempty"`
	SubscriptionsSocket string `json:"subscriptions_socket,omitempty"`
}

func (in networkStart) folders() serveFolders {
	return serveFolders{dsn: in.DatabaseURL, files: in.FilesDir, index: in.IndexDir, subsDir: in.SubscriptionsDir, gjConfig: in.GraphJinConfig,
		duckdb: in.DuckDB, checkSocket: in.CheckSocket, gjSocket: in.GraphJinSocket, subsSocket: in.SubscriptionsSocket}
}

type networkReady struct {
	Confinement *confine.State `json:"confinement,omitempty"`
	Error       string         `json:"error,omitempty"`
}

type networkPart struct {
	state  confine.State
	done   <-chan error
	exited <-chan struct{}
	stdin  io.Closer
	proc   *os.Process
}

// stop closes the part's stdin, which ends it, and kills it after 8 s.
func (p *networkPart) stop() {
	_ = p.stdin.Close()
	select {
	case <-p.exited:
	case <-time.After(8 * time.Second):
		_ = p.proc.Kill()
		<-p.exited
	}
}

// networkEnv drops the sandbox key and every variable that can hold a model
// key, a database URL or the secret key. The AWS pair stays for the bucket.
func networkEnv(environ []string) []string {
	drop := map[string]bool{"OPENNEKO_SANDBOX_KEY": true, serveCredentialsEnv: true, native.SupervisorFDEnv: true}
	for _, name := range serve.KeyEnvNames(environ) {
		if name != "AWS_SECRET_ACCESS_KEY" && name != "AWS_SESSION_TOKEN" {
			drop[name] = true
		}
	}
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if !drop[name] {
			out = append(out, kv)
		}
	}
	return out
}

// startNetworkPart starts `openneko serve-network`, gives it its logins and
// waits until it is confined and listens.
func startNetworkPart(ctx context.Context, stdout, stderr io.Writer, in networkStart) (*networkPart, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, serveNetworkCmd)
	cmd.Env = networkEnv(os.Environ())
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start the network part of openneko serve: %w", err)
	}
	done := make(chan error, 1)
	exited := make(chan struct{})
	part := &networkPart{done: done, exited: exited, stdin: stdin, proc: cmd.Process}
	raw, err := json.Marshal(in)
	if err == nil {
		_, err = stdin.Write(append(raw, '\n'))
	}
	clear(raw)
	r := bufio.NewReader(out)
	lines := make(chan string, 1)
	go func() {
		for {
			line, err := r.ReadString('\n')
			var ready networkReady
			if err != nil || (json.Unmarshal([]byte(line), &ready) == nil && (ready.Confinement != nil || ready.Error != "")) {
				lines <- line
				break
			}
			_, _ = io.WriteString(stdout, line)
		}
		_, _ = io.Copy(stdout, r)
		werr := cmd.Wait()
		if werr == nil {
			werr = errors.New("it exited")
		}
		done <- werr
		close(exited)
	}()
	if err != nil {
		part.stop()
		return nil, fmt.Errorf("give the network part of openneko serve its logins: %w", err)
	}
	var ready networkReady
	select {
	case line := <-lines:
		if json.Unmarshal([]byte(line), &ready) != nil || (ready.Confinement == nil && ready.Error == "") {
			ready.Error = "it ended before it was ready"
		}
	case <-ctx.Done():
		ready.Error = ctx.Err().Error()
	case <-time.After(2 * time.Minute):
		ready.Error = "it was not ready after 2 minutes"
	}
	if ready.Error != "" {
		part.stop()
		return nil, fmt.Errorf("the network part of openneko serve: %s", ready.Error)
	}
	part.state = *ready.Confinement
	return part, nil
}

func newServeNetworkCmd() *cobra.Command {
	return &cobra.Command{
		Use:    serveNetworkCmd,
		Short:  "Run the network part of openneko serve (started by serve; reads its logins on stdin)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := native.NotifyStop(cmd.Context())
			defer stop()
			stdin := bufio.NewReader(os.Stdin)
			line, err := stdin.ReadBytes('\n')
			var in networkStart
			if err == nil {
				err = json.Unmarshal(line, &in)
			}
			clear(line)
			if err != nil {
				return fmt.Errorf("read the start of the network part: %w", err)
			}
			go func() {
				_, _ = io.Copy(io.Discard, stdin)
				stop()
			}()
			ready := json.NewEncoder(cmd.OutOrStdout())
			err = runNetworkPart(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), in, func(st confine.State) { _ = ready.Encode(networkReady{Confinement: &st}) })
			if err != nil {
				_ = ready.Encode(networkReady{Error: err.Error()})
			}
			return err
		},
	}
}

func runNetworkPart(ctx context.Context, stdout, stderr io.Writer, in networkStart, ready func(confine.State)) error {
	confinement, err := confineServe(stderr, in.folders(), webfront.Development())
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, db.ConnConfig{URL: in.DatabaseURL}, "openneko serve", nil)
	if err != nil {
		return err
	}
	defer pool.Close()
	store, err := filestore.Open(ctx, filestore.FromEnv(in.FilesDir))
	if err != nil {
		return err
	}
	reportFileStore(ctx, stderr, store, in.FilesDir)
	ix, err := filecontent.Open(pool, in.IndexDir, 0)
	if err != nil {
		return err
	}
	svc := files.New(store, ix, in.TokenKey, files.Options{DuckDB: in.DuckDB})

	ln, err := net.Listen("tcp", in.Addr)
	if err != nil {
		return err
	}
	var checkDone <-chan error
	if in.CheckSocket != "" && in.GraphJinConfig != "" {
		if checkDone, err = serveConfigCheck(ctx, stdout, in.CheckSocket, in.GraphJinConfig); err != nil {
			_ = ln.Close()
			return err
		}
	}
	gjDone := make(chan error, 1)
	if in.GraphJinConfig != "" {
		gj := &gjhost.Supervisor{ConfigFile: in.GraphJinConfig, Addr: in.GraphJinAddr, Log: stderr, Command: gjhost.SelfCommand(in.GraphJinConfig), Socket: in.GraphJinSocket}
		go func() { gjDone <- gj.Run(ctx) }()
	} else {
		gjDone <- nil
	}
	subsDone := make(chan error, 1)
	if in.SubscriptionsAddr != "" && in.SubscriptionsURL != "" {
		subs := &gjhost.Subscriptions{URL: in.SubscriptionsURL, Dir: in.SubscriptionsDir, Addr: in.SubscriptionsAddr, Log: stderr, Socket: in.SubscriptionsSocket}
		go func() { subsDone <- subs.Run(ctx) }()
	} else {
		subsDone <- nil
	}
	srv := &http.Server{Handler: svc.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ready(confinement)
	fmt.Fprintf(stdout, "openneko serve listening on %s\n", ln.Addr())
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	var runErr error
	select {
	case runErr = <-errc:
	case err := <-checkDone:
		runErr = fmt.Errorf("config check socket: %w", err)
	case <-ctx.Done():
	}
	shut, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Shutdown(shut); err != nil && !errors.Is(err, context.DeadlineExceeded) && runErr == nil {
		runErr = err
	}
	wait := time.After(7 * time.Second)
	for _, done := range []chan error{gjDone, subsDone} {
		select {
		case <-done:
		case <-wait:
		}
	}
	return runErr
}
