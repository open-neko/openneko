// harness accepts one JSON run specification and emits NDJSON lifecycle events.
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

	"github.com/open-neko/harness/internal/agent"
	"github.com/open-neko/harness/internal/session"
)

func Main(lookup func(context.Context, string) (json.RawMessage, error)) {
	MainWithTools(agent.Tools{Lookup: lookup})
}

func MainWithTools(tools agent.Tools) {
	MainWithToolsAndCleanup(tools, nil)
}

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
	return execute(ctx, input, output, nil)
}
func execute(ctx context.Context, input io.Reader, output io.Writer, lookup func(context.Context, string) (json.RawMessage, error)) (int, error) {
	return executeWithTools(ctx, input, output, agent.Tools{Lookup: lookup})
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
	if spec.HostRoutingDigest != "" {
		return 2, fmt.Errorf("host routing digest cannot be selected by run input")
	}
	client, digest, err := loadModelClient(os.Getenv)
	if err != nil {
		return 2, err
	}
	if spec.TriageSummary != "" {
		tools.Triage, err = loadTriageClient(os.Getenv("HARNESS_MODEL_ROUTES"), os.Getenv)
		if err != nil {
			return 2, err
		}
	}
	spec.HostRoutingDigest = digest
	encoder := json.NewEncoder(output)
	emit := func(e agent.Event) error { return encoder.Encode(e) }
	var result agent.Result
	resume := os.Getenv("HARNESS_RESUME")
	if resume != "" && resume != "1" {
		return 2, fmt.Errorf("HARNESS_RESUME must be unset or 1")
	}
	if root := os.Getenv("HARNESS_STATE_DIR"); root != "" {
		if resume == "1" {
			result, err = session.ResumeWithTools(ctx, root, spec, client, tools, emit)
		} else {
			result, err = session.RunWithTools(ctx, root, spec, client, tools, emit)
		}
	} else {
		if resume != "" {
			return 2, fmt.Errorf("continuation requires HARNESS_STATE_DIR")
		}
		if tools.Lookup != nil || tools.Propose != nil || len(tools.Capabilities) != 0 || spec.TriageSummary != "" {
			return 2, fmt.Errorf("tool execution requires HARNESS_STATE_DIR")
		}
		result, err = agent.RunWithTools(ctx, spec, client, tools, emit)
	}
	if err != nil {
		return 1, fmt.Errorf("run input or event delivery failed")
	}
	if result.Status != "completed" {
		return 1, nil
	}
	return 0, nil
}
