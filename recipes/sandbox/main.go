package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	flags := flag.NewFlagSet("openneko-sandbox", flag.ExitOnError)
	listen := flags.String("listen", envOr("OPENNEKO_SANDBOX_LISTEN", "unix:///run/openneko/sandbox.sock"), "unix:///path or host:port")
	_ = flags.Parse(os.Args[1:])
	server := &Server{
		Workspaces:  envOr("OPENNEKO_SANDBOX_WORKSPACES", "/var/lib/openneko/sandbox-runs"),
		Homes:       envOr("OPENNEKO_SANDBOX_HOMES", "/var/lib/openneko/sandbox"),
		Key:         os.Getenv("OPENNEKO_SANDBOX_KEY"),
		BaseUID:     intEnv("OPENNEKO_SANDBOX_BASE_UID", defaultBaseUID),
		Users:       intEnv("OPENNEKO_SANDBOX_USERS", defaultUsers),
		GID:         intEnv("OPENNEKO_SANDBOX_GID", os.Getgid()),
		SwitchUsers: os.Geteuid() == 0,
		PATH:        envOr("PATH", "/usr/local/bin:/usr/bin:/bin"),
		NodePath:    os.Getenv("NODE_PATH"),
	}
	if !server.SwitchUsers {
		fmt.Fprintln(os.Stderr, "openneko sandbox: not root, so commands run as this user")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.ListenAndServe(ctx, *listen); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
