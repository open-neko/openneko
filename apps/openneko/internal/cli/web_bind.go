package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/compose"
	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/db"
	"github.com/open-neko/neko/apps/openneko/internal/webfront"
)

func loopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && ip.IsLoopback()
}

func signInLive(ctx context.Context, pool *pgxpool.Pool) bool {
	return (&webfront.Gate{SecretsDir: config.Dir(""), DB: pool}).Status(ctx).Live()
}

// webListenAddr keeps web on the loopback address while no sign-in provider
// is live, because every request then acts as the first admin. An address
// from the command line is refused; a configured one waits for sign-in.
func webListenAddr(addr string, fromFlag, live, inContainer bool) (string, string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", "", fmt.Errorf("--addr %q: %w", addr, err)
	}
	if live || inContainer || loopbackHost(host) {
		return addr, "", nil
	}
	local := net.JoinHostPort("127.0.0.1", port)
	if fromFlag {
		return "", "", fmt.Errorf("sign-in is not set up, so web listens on loopback only: set up sign-in at http://%s first, then start web on %s", net.JoinHostPort("localhost", port), addr)
	}
	return local, fmt.Sprintf("openneko web: sign-in is not set up, so web listens on %s only; set up sign-in at http://%s, then restart OpenNeko to listen on %s", local, net.JoinHostPort("localhost", port), addr), nil
}

func signInLiveHere(ctx context.Context) bool {
	conn, err := defaultConn()
	if err != nil {
		return false
	}
	pool, err := db.Open(ctx, conn, "openneko sign-in-state", defaultConn)
	if err != nil {
		return false
	}
	defer pool.Close()
	return signInLive(ctx, pool)
}

func newSignInStateCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "sign-in-state",
		Short:  "Print on when a sign-in provider is live, else off",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			state := "off"
			if signInLiveHere(cmd.Context()) {
				state = "on"
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), state)
			return err
		},
	}
}

var dockerSignInState = func(ctx context.Context, sup *compose.Supervisor, project string, files, runFlags []string) (string, error) {
	var out bytes.Buffer
	args := append(append([]string{"run", "--rm", "--no-deps", "-T"}, runFlags...), "web", "openneko", "sign-in-state")
	if _, err := sup.RunIO(ctx, project, files, args, nil, &out, os.Stderr); err != nil {
		return "", err
	}
	lines := strings.Fields(out.String())
	if len(lines) == 0 {
		return "", nil
	}
	return lines[len(lines)-1], nil
}

// publishWebForSignIn publishes the web port of a Docker install on
// 127.0.0.1 while no sign-in provider is live. It returns a function that
// puts OPENNEKO_WEB_BIND_ADDRESS back.
func publishWebForSignIn(ctx context.Context, out io.Writer, sup *compose.Supervisor, project string, files, runFlags []string) (func(), error) {
	keep := func() {}
	bind := strings.TrimSpace(os.Getenv("OPENNEKO_WEB_BIND_ADDRESS"))
	if bind == "" {
		bind = "0.0.0.0"
	}
	if loopbackHost(bind) {
		return keep, nil
	}
	state, err := dockerSignInState(ctx, sup, project, files, runFlags)
	if err != nil {
		return keep, err
	}
	if state == "on" {
		return keep, nil
	}
	previous, had := os.LookupEnv("OPENNEKO_WEB_BIND_ADDRESS")
	if err := os.Setenv("OPENNEKO_WEB_BIND_ADDRESS", "127.0.0.1"); err != nil {
		return keep, err
	}
	port := envOr("OPENNEKO_PORT", "3000")
	fmt.Fprintf(out, "Sign-in is not set up, so OpenNeko publishes the web port on 127.0.0.1:%s only. Set up sign-in at http://localhost:%s, then run `openneko start` again to publish it on %s.\n", port, port, bind)
	return func() {
		if had {
			_ = os.Setenv("OPENNEKO_WEB_BIND_ADDRESS", previous)
		} else {
			_ = os.Unsetenv("OPENNEKO_WEB_BIND_ADDRESS")
		}
	}, nil
}
