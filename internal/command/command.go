// harness accepts one JSON run specification and emits NDJSON lifecycle events.
package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	ax "github.com/ax-llm/ax/packages/go"
	"github.com/open-neko/harness/internal/agent"
	"github.com/open-neko/harness/internal/session"
)

func Main(lookup func(context.Context, string) (json.RawMessage, error)) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code, err := execute(ctx, os.Stdin, os.Stdout, lookup)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}
func run(ctx context.Context, input io.Reader, output io.Writer) (int, error) {
	return execute(ctx, input, output, nil)
}
func execute(ctx context.Context, input io.Reader, output io.Writer, lookup func(context.Context, string) (json.RawMessage, error)) (int, error) {
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
	base := os.Getenv("HARNESS_MODEL_URL")
	model := os.Getenv("HARNESS_MODEL")
	key := os.Getenv("HARNESS_MODEL_API_KEY")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || model == "" || key == "" {
		return 2, fmt.Errorf("configure HARNESS_MODEL_URL, HARNESS_MODEL and HARNESS_MODEL_API_KEY")
	}
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", base, "api_key", key, "model", model))
	encoder := json.NewEncoder(output)
	emit := func(e agent.Event) error { return encoder.Encode(e) }
	var result agent.Result
	if root := os.Getenv("HARNESS_STATE_DIR"); root != "" {
		result, err = session.Run(ctx, root, spec, client, lookup, emit)
	} else {
		result, err = agent.Run(ctx, spec, client, lookup, emit)
	}
	if err != nil {
		return 1, fmt.Errorf("run input or event delivery failed")
	}
	if result.Status != "completed" {
		return 1, nil
	}
	return 0, nil
}
