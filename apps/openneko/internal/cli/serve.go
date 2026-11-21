package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/appdir"
	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/files"
	"github.com/open-neko/neko/apps/openneko/internal/filestore"
	"github.com/open-neko/neko/apps/openneko/internal/native"
	"github.com/open-neko/neko/apps/openneko/internal/secretenv"
	"github.com/open-neko/neko/apps/openneko/internal/webfront"
	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
	"github.com/open-neko/openneko/apps/ax-harness/ownacl"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

func newServeCmd() *cobra.Command {
	var addr, filesDir, indexDir, duckdb, agentSocket, checkSocket, sandboxURL, credentials, gjConfig, gjAddr, subsAddr, subsDir string
	var maxTurns int
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the OpenNeko core service (user files, file search, agent turns and the customer GraphJin)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := native.NotifyStop(cmd.Context())
			defer stop()
			native.FollowSupervisor()
			if err := secretenv.ReadHanded(os.Stdin); err != nil {
				return err
			}
			development := webfront.Development()

			if credentials == "" && !development {
				return errors.New("openneko serve needs --credentials from `openneko serve-init` in production mode, so that no part of serve holds the secret-key or the main database login")
			}
			key, dsn, err := serveKeyAndDatabase(credentials)
			if err != nil {
				return err
			}
			in := networkStart{TokenKey: key, DatabaseURL: dsn, Addr: addr, FilesDir: filesDir, IndexDir: indexDir, DuckDB: duckdb,
				CheckSocket: checkSocket, GraphJinConfig: gjConfig, GraphJinAddr: gjAddr, SubscriptionsDir: subsDir, FileStore: filestore.FromEnv(filesDir)}
			sandboxSocket, _ := sandbox.SocketPath(sandboxURL)
			if err := networkSocketFolder(checkSocket, agentSocket, keydoor.SocketPath(os.Getenv, agentSocket), sandboxSocket, webAdminSocket()); err != nil {
				return err
			}
			tmp, err := serveTemp(os.TempDir())
			if err != nil {
				return err
			}
			defer os.RemoveAll(tmp.Base)
			in.TempDir = tmp.Network
			sockDir := tmp.Sockets
			if gjConfig != "" {
				in.GraphJinSocket = filepath.Join(sockDir, "gj.sock")
			}
			if subsAddr != "" && credentials != "" {
				raw, err := os.ReadFile(filepath.Join(credentials, serveSubsFile))
				if err != nil {
					return fmt.Errorf("read the subscriptions login: %w", err)
				}
				in.SubscriptionsAddr, in.SubscriptionsURL = subsAddr, strings.TrimSpace(string(raw))
				in.SubscriptionsSocket = filepath.Join(sockDir, "subs.sock")
			} else if subsAddr != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), "subscriptions GraphJin off: it needs --credentials from `openneko serve-init`")
			}
			network, err := startNetworkPart(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), in)
			clear(key)
			if err != nil {
				return err
			}
			defer network.stop()
			agentPeerRefused = refuseNetworkPeers(network.proc.Pid)

			if err := os.Setenv("TMPDIR", tmp.Turns); err != nil {
				return err
			}
			hostState, start, err := confineTurnHost(cmd.ErrOrStderr(), serveFolders{agentRuns: webfront.AgentRunsDir(), agentSocket: agentSocket, sandboxURL: sandboxURL}, development)
			if err != nil {
				return err
			}
			var agentDone <-chan error
			if agentSocket != "" {
				if agentDone, err = serveAgentTurns(ctx, cmd.OutOrStdout(), agentSocket, maxTurns, start, hostState, network.state); err != nil {
					return err
				}
			}
			select {
			case err := <-network.done:
				stop()
				return fmt.Errorf("the network part of openneko serve stopped: %w", err)
			case err := <-agentDone:
				stop()
				return fmt.Errorf("agent socket: %w", err)
			case <-ctx.Done():
			}
			if agentDone != nil {
				select {
				case <-agentDone:
				case <-time.After(time.Second):
				}
			}
			return nil
		},
	}
	data := appdir.Data()
	cmd.Flags().StringVar(&addr, "addr", envOr("OPENNEKO_SERVE_ADDR", "127.0.0.1:8090"), "Listen address")
	cmd.Flags().StringVar(&filesDir, "files-dir", envOr("OPENNEKO_FILES_DIR", filepath.Join(data, "files")), "Folder that holds each user's private files. OPENNEKO_FILES_BUCKET=s3://bucket[/prefix] keeps them in a bucket instead")
	cmd.Flags().StringVar(&indexDir, "index-dir", envOr("OPENNEKO_FILES_INDEX_DIR", filepath.Join(data, "file-index")), "Folder for the sheet tables derived from the files; safe to delete")
	cmd.Flags().StringVar(&agentSocket, "agent-socket", os.Getenv("OPENNEKO_SERVE_SOCKET"), "Unix socket for agent turns; empty turns them off")
	cmd.Flags().StringVar(&checkSocket, "config-check-socket", os.Getenv("OPENNEKO_CONFIG_CHECK_SOCKET"), "Unix socket where web asks serve to load a candidate GraphJin config with the pinned loader; empty turns the check off")
	cmd.Flags().StringVar(&sandboxURL, "sandbox-url", os.Getenv("OPENNEKO_SANDBOX_URL"), "Sandbox service for agent terminal commands (unix:///path or http(s)://host:port); empty turns the terminal off. Its key is read from OPENNEKO_SANDBOX_KEY")
	cmd.Flags().IntVar(&maxTurns, "max-turns", envInt("OPENNEKO_SERVE_MAX_TURNS", 32), "Agent turns that run at once; more are refused")
	cmd.Flags().StringVar(&credentials, "credentials", os.Getenv(serveCredentialsEnv), "Folder written by `openneko serve-init`: the token key and the files role database URL. Production mode needs it; in development mode, empty reads the secret-key and the main database login")
	cmd.Flags().StringVar(&gjConfig, "graphjin-config", os.Getenv("OPENNEKO_GRAPHJIN_CONFIG"), "Customer GraphJin config file (agentic.yml); empty runs no GraphJin. GraphJin runs as a child process; a new token in .openneko-graphjin-restart beside it restarts the child")
	cmd.Flags().StringVar(&gjAddr, "graphjin-addr", envOr("OPENNEKO_GRAPHJIN_ADDR", "127.0.0.1:8080"), "Listen address for the customer GraphJin")
	cmd.Flags().StringVar(&subsAddr, "subscriptions-addr", envOr("OPENNEKO_SUBSCRIPTIONS_ADDR", "127.0.0.1:8089"), "Listen address for the subscriptions GraphJin, which serves workflow output subscriptions as the subscriptions role; empty turns it off")
	cmd.Flags().StringVar(&subsDir, "subscriptions-dir", subscriptionsDir(), "Private folder for the subscriptions GraphJin config and state")
	cmd.Flags().StringVar(&duckdb, "duckdb", envOr("OPENNEKO_DUCKDB", "duckdb"), "DuckDB CLI for SQL over sheet tables")
	return cmd
}

// serveTempDirs are the private temp folders of serve: one for the sockets
// of the network part, one for each part's own temp files.
type serveTempDirs struct{ Base, Sockets, Network, Turns string }

// serveTemp makes serve's temp folders under root. Each part of serve
// confines itself to its own folder, never to all of root.
func serveTemp(root string) (serveTempDirs, error) {
	base, err := os.MkdirTemp(root, "openneko-serve-")
	if err != nil {
		return serveTempDirs{}, err
	}
	if err := ownacl.Set(base); err != nil {
		_ = os.RemoveAll(base)
		return serveTempDirs{}, err
	}
	d := serveTempDirs{Base: base, Sockets: filepath.Join(base, "sockets"), Network: filepath.Join(base, "network-tmp"), Turns: filepath.Join(base, "turn-tmp")}
	for _, dir := range []string{d.Sockets, d.Network, d.Turns} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			_ = os.RemoveAll(base)
			return serveTempDirs{}, err
		}
	}
	return d, nil
}

func serveKeyAndDatabase(credentials string) ([]byte, string, error) {
	if credentials != "" {
		key, dsn, err := readServeCredentials(credentials)
		if err != nil {
			return nil, "", fmt.Errorf("read the serve credentials: %w", err)
		}
		return key, dsn, nil
	}
	key, err := config.SigningSecret("", files.TokenLabel)
	if err != nil {
		return nil, "", fmt.Errorf("read the secret-key: %w", err)
	}
	conn, err := defaultConn()
	if err != nil {
		return nil, "", err
	}
	return key, conn.DSN(), nil
}

func subscriptionsDir() string {
	return envOr("OPENNEKO_SUBSCRIPTIONS_DIR", filepath.Join(appdir.Data(), "subscriptions"))
}

func reportFileStore(ctx context.Context, w io.Writer, store filestore.Store, dir string) {
	if err := store.Check(ctx); err != nil {
		fmt.Fprintf(w, "file store: cannot reach it: %v. The file tools fail until serve can reach it\n", err)
	} else {
		fmt.Fprintf(w, "file store: %s\n", store.Describe())
	}
	if store.Name() == "local" {
		return
	}
	if n := filestore.LocalFilesLeft(dir, 1000); n > 0 {
		count := fmt.Sprintf("%d files", n)
		if n == 1 {
			count = "1 file"
		} else if n >= 1000 {
			count = "1000 or more files"
		}
		fmt.Fprintf(w, "file store: the local folder %s still holds %s, and serve does not read them. To copy them to the bucket, run `openneko files copy-to-bucket`. When the bucket holds them, remove them from the folder\n", dir, count)
	}
}
