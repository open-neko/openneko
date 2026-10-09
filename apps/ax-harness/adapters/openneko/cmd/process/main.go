// harness-process is the host-side entry point for an isolated OpenShell task.
// The trusted launcher supplies all sandbox and filesystem bindings through its
// environment. Standard input contains only argv and declared output names.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/adapters/openneko/processshell"
)

const maxRequestBytes = 32 << 10

type response struct {
	OK     bool                 `json:"ok"`
	Result *processshell.Result `json:"result,omitempty"`
	Error  string               `json:"error,omitempty"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv, os.Stdin, os.Stdout); err != nil {
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, input io.Reader, output io.Writer) error {
	encoded, err := io.ReadAll(io.LimitReader(input, maxRequestBytes+1))
	if err != nil || len(encoded) > maxRequestBytes {
		return sendError(output, "invalid bounded process request")
	}
	var req processshell.Request
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return sendError(output, "invalid process request")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return sendError(output, "invalid process request")
	}
	op, err := strconv.Atoi(getenv("HARNESS_PROCESS_OPERATION_ID"))
	if err != nil {
		return sendError(output, "invalid process operation binding")
	}
	timeout := 120
	if value := getenv("HARNESS_PROCESS_TIMEOUT_SECONDS"); value != "" {
		timeout, err = strconv.Atoi(value)
		if err != nil {
			return sendError(output, "invalid process timeout binding")
		}
	}
	options := processshell.Options{
		CLI: getenv("HARNESS_OPENSHELL_BIN"), Gateway: getenv("OPENSHELL_GATEWAY"),
		Image: getenv("HARNESS_PROCESS_IMAGE"), RunID: getenv("HARNESS_PROCESS_RUN_ID"),
		OperationID: op, InputRoot: getenv("HARNESS_PROCESS_INPUT_ROOT"),
		OutputRoot: getenv("HARNESS_PROCESS_OUTPUT_ROOT"), TimeoutSeconds: timeout,
		CPU: getenv("HARNESS_PROCESS_CPU"), Memory: getenv("HARNESS_PROCESS_MEMORY"),
	}
	runner, err := processshell.New(options)
	if err != nil {
		return sendError(output, "invalid process compartment binding")
	}
	deadline := time.Duration(options.TimeoutSeconds+120) * time.Second
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	result, err := runner.Run(ctx, req)
	if err != nil {
		// A cancellation or teardown failure has the same external outcome:
		// the host must inspect its durable operation and artifact receipts.
		message := "process execution failed"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			message = "process execution cancelled"
		}
		return sendError(output, message)
	}
	return json.NewEncoder(output).Encode(response{OK: true, Result: &result})
}

func sendError(output io.Writer, message string) error {
	if err := json.NewEncoder(output).Encode(response{Error: message}); err != nil {
		return fmt.Errorf("process receipt unavailable: %w", err)
	}
	return errors.New(message)
}
