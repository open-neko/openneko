// harness-inspect reads recovery evidence without model credentials or execution.
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
	if len(os.Args) != 1 {
		return fmt.Errorf("invalid inspector arguments")
	}

	data, err := io.ReadAll(io.LimitReader(os.Stdin, 131073))
	if err != nil || len(data) > 131072 {
		return fmt.Errorf("invalid recovery input")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var spec agent.Spec
	if decoder.Decode(&spec) != nil {
		return fmt.Errorf("invalid recovery input")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fmt.Errorf("expected one recovery specification")
	}
	result, err := session.Inspect(os.Getenv("HARNESS_STATE_DIR"), spec)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
