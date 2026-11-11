package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/open-neko/openneko/apps/ax-harness/embedding"
	"github.com/open-neko/openneko/apps/ax-harness/inference"
)

func generateMain(in io.Reader, out, errOut io.Writer, key, fallbackKey string) int {
	var request inference.Request
	return callMain(in, out, errOut, "expected a generate request", &request, func(ctx context.Context) (any, error) {
		return inference.Generate(ctx, request, key, fallbackKey)
	})
}

func embedMain(in io.Reader, out, errOut io.Writer, key string) int {
	var request embedding.Request
	return callMain(in, out, errOut, "expected an embed request", &request, func(ctx context.Context) (any, error) {
		return embedding.Embed(ctx, request, key)
	})
}

func callMain(in io.Reader, out, errOut io.Writer, usage string, request any, run func(context.Context) (any, error)) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := json.NewDecoder(io.LimitReader(in, 32<<20)).Decode(request); err != nil {
		fmt.Fprintln(errOut, usage)
		return 2
	}
	result, err := run(ctx)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}
