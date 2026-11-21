package cli

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"strings"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/dockerproxy"
)

type unconfinedReply struct {
	Setting struct {
		On       bool   `json:"on"`
		Host     string `json:"host"`
		Kernel   string `json:"kernel"`
		Reason   string `json:"reason"`
		HostUser string `json:"hostUser"`
		At       string `json:"at"`
	} `json:"setting"`
	Applies    bool   `json:"applies"`
	NotApplied string `json:"notApplied"`
}

func newConfinementCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "confinement",
		Short: "Allow or deny agent turns without OS confinement on this host",
		Long: "Agent turns run only in a runner that the OS confines. On a host that cannot confine them, the operator of the host may allow them " +
			"without confinement. The override holds only for this host and its kernel, and only while the host cannot confine. " +
			"Run it as the account that owns the install, or as root for a server install. The web app shows the state and cannot change it.",
	}
	var reason, hostUser string
	allow := &cobra.Command{
		Use:   "allow-unconfined",
		Short: "Allow agent turns without OS confinement on this host, which cannot confine them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return setUnconfined(cmd, true, reason, hostUser)
		},
	}
	allow.Flags().StringVar(&reason, "reason", "", "Why this host cannot confine agent turns and why turns must run; the audit chain keeps it")
	deny := &cobra.Command{
		Use:   "deny",
		Short: "Refuse agent turns without OS confinement again",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return setUnconfined(cmd, false, reason, hostUser)
		},
	}
	deny.Flags().StringVar(&reason, "reason", "", "Why the override ends; the audit chain keeps it")
	status := &cobra.Command{
		Use:   "status",
		Short: "Show whether agent turns may run without OS confinement on this host",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if code, proxied := MaybeProxyToWeb(cmd); proxied {
				return WithExit(code, nil)
			}
			on, line, err := unconfinedTurnsState(cmd.Context(), webAdminSocket())
			if err != nil {
				return err
			}
			switch {
			case on:
				fmt.Fprintln(cmd.OutOrStdout(), line)
			case line != "":
				fmt.Fprintf(cmd.OutOrStdout(), "agent turns without OS confinement: allowed, but the override does not apply: %s\n", line)
			default:
				fmt.Fprintln(cmd.OutOrStdout(), "agent turns without OS confinement: denied")
			}
			return nil
		},
	}
	for _, c := range []*cobra.Command{allow, deny} {
		c.Flags().StringVar(&hostUser, "host-user", "", "")
		_ = c.Flags().MarkHidden("host-user")
	}
	cmd.AddCommand(allow, deny, status)
	return cmd
}

// setUnconfined sends the operator's choice to web's admin socket. In a
// Docker install it runs inside the web container and names the host user
// that ran it.
func setUnconfined(cmd *cobra.Command, on bool, reason, hostUser string) error {
	if hostUser == "" {
		hostUser = currentHostUser()
	}
	if on && strings.TrimSpace(reason) == "" {
		return errors.New(`give the reason with --reason, for example: openneko confinement allow-unconfined --reason "the kernel has no Landlock"`)
	}
	if code, proxied := MaybeProxyToWeb(cmd, func(container string) int {
		return dockerproxy.ProxyTo(container, append(os.Args[1:], "--host-user="+hostUser))
	}); proxied {
		return WithExit(code, nil)
	}
	var reply unconfinedReply
	if _, err := requestWebAdminAPI(cmd.Context(), "confinement", http.MethodPost, "/admin/confinement/unconfined",
		map[string]any{"on": on, "reason": reason, "hostUser": hostUser}, &reply); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if !on {
		fmt.Fprintln(out, "agent turns without OS confinement: denied. A host that cannot confine runs no agent turn")
		return nil
	}
	fmt.Fprintf(out, "agent turns without OS confinement: allowed on %s (%s) by %s. The agent can read the files and reach the network of the OpenNeko account\n",
		reply.Setting.Host, reply.Setting.Kernel, reply.Setting.HostUser)
	if !reply.Applies {
		fmt.Fprintf(out, "the override does not apply yet: %s\n", reply.NotApplied)
	}
	return nil
}

func currentHostUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	for _, name := range []string{"USER", "USERNAME"} {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return fmt.Sprintf("uid %d", os.Getuid())
}
