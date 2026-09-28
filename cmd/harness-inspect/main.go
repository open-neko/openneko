// harness-inspect inspects or reconciles trusted recovery evidence without model execution.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/open-neko/harness/internal/agent"
	"github.com/open-neko/harness/internal/command"
	"github.com/open-neko/harness/internal/session"
)

func main() {
	if err := inspect(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func inspect() error {
	if len(os.Args) == 3 && os.Args[1] == "--lock" {
		root := os.Args[2]
		if err := os.MkdirAll(root, 0700); err != nil {
			return err
		}
		lock, err := os.OpenFile(filepath.Join(root, "launcher.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		defer lock.Close()
		if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
			return fmt.Errorf("Harness launcher still active; reconciliation refused")
		}
		defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		if _, err := fmt.Fprintln(os.Stdout, "locked"); err != nil {
			return err
		}
		_, err = io.Copy(io.Discard, os.Stdin) // Parent exit closes the pipe, releasing ownership.
		return err
	}
	reconcile := len(os.Args) == 2 && os.Args[1] == "--reconcile"
	if len(os.Args) != 1 && !reconcile {
		return fmt.Errorf("invalid inspector arguments")
	}

	limit := int64(131072)
	if reconcile {
		limit = 2 << 20
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, limit+1))
	if err != nil || int64(len(data)) > limit {
		return fmt.Errorf("invalid recovery input")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var spec agent.Spec
	var request struct {
		Spec     agent.Spec        `json:"spec"`
		Receipts []session.Receipt `json:"receipts"`
	}
	var target any = &spec
	if reconcile {
		target = &request
	}
	if decoder.Decode(target) != nil {
		return fmt.Errorf("invalid recovery input")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fmt.Errorf("expected one recovery specification")
	}
	digest, err := command.RoutingDigest(os.Getenv("HARNESS_MODEL_ROUTES"))
	if err != nil {
		return err
	}
	if reconcile {
		if request.Spec.HostRoutingDigest != "" {
			return fmt.Errorf("host routing digest cannot be selected by recovery input")
		}
		request.Spec.HostRoutingDigest = digest
	} else {
		if spec.HostRoutingDigest != "" {
			return fmt.Errorf("host routing digest cannot be selected by recovery input")
		}
		spec.HostRoutingDigest = digest
	}
	var result session.Recovery
	if reconcile {
		result, err = session.Reconcile(os.Getenv("HARNESS_STATE_DIR"), request.Spec, request.Receipts)
	} else {
		result, err = session.Inspect(os.Getenv("HARNESS_STATE_DIR"), spec)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
