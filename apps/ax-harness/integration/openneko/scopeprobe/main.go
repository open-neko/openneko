// scopeprobe checks the admission boundary against a checkpoint produced by
// a connected OpenNeko/OpenShell run. It must never contact a model or broker.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/open-neko/openneko/apps/ax-harness/internal/agent"
	"github.com/open-neko/openneko/apps/ax-harness/internal/session"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: scopeprobe STATE_DIR RUN_ID FOREIGN_ORG_ID")
		os.Exit(2)
	}
	root, runID, foreignOrgID := os.Args[1], os.Args[2], os.Args[3]
	sum := sha256.Sum256([]byte(runID))
	data, err := os.ReadFile(filepath.Join(root, hex.EncodeToString(sum[:])+".json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var checkpoint struct {
		Spec agent.Spec `json:"spec"`
	}
	if err := json.Unmarshal(data, &checkpoint); err != nil || checkpoint.Spec.RunID != runID {
		fmt.Fprintln(os.Stderr, "invalid connected checkpoint")
		os.Exit(1)
	}
	events := 0
	_, err = session.RunWithTools(context.Background(), root, checkpoint.Spec, nil,
		agent.Tools{Scope: "org:" + foreignOrgID}, func(agent.Event) error { events++; return nil })
	if err == nil || !strings.Contains(err.Error(), "run admission scope changed") || events != 0 {
		fmt.Fprintf(os.Stderr, "foreign scope replay was not denied before events: err=%v events=%d\n", err, events)
		os.Exit(1)
	}
	fmt.Println("M6_CONNECTED_FOREIGN_SCOPE_REPLAY_DENIED")
}
