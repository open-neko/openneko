// Package command accepts one JSON run specification and emits NDJSON lifecycle events.
package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/open-neko/openneko/apps/ax-harness/internal/agent"
)

// MainWithToolsAndCleanup closes external tool sessions before exiting.
func MainWithToolsAndCleanup(tools agent.Tools, cleanup func() error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code, err := executeWithTools(ctx, os.Stdin, os.Stdout, tools)
	if cleanup != nil {
		if closeErr := cleanup(); closeErr != nil {
			code = 1
			err = fmt.Errorf("tool session cleanup failed: %w", closeErr)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}

func run(ctx context.Context, input io.Reader, output io.Writer) (int, error) {
	return executeWithTools(ctx, input, output, agent.Tools{})
}

func executeWithTools(ctx context.Context, input io.Reader, output io.Writer, tools agent.Tools) (int, error) {
	var spec agent.Spec
	data, readErr := io.ReadAll(io.LimitReader(input, 131073))
	if readErr != nil || len(data) > 131072 {
		return 2, fmt.Errorf("run input exceeds limit or could not be read")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return 2, fmt.Errorf("invalid JSON run specification")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return 2, fmt.Errorf("expected exactly one run specification")
	}
	client, err := loadModelClient(os.Getenv)
	if err != nil {
		return 2, err
	}
	encoder := json.NewEncoder(output)
	result, err := agent.RunWithTools(ctx, spec, client, tools, func(e agent.Event) error { return encoder.Encode(e) })
	if err != nil {
		return 1, fmt.Errorf("run input or event delivery failed")
	}
	if result.Status != "completed" {
		return 1, nil
	}
	return 0, nil
}
