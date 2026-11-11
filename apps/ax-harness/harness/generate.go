package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/open-neko/openneko/apps/ax-harness/inference"
)

func generateMain(in io.Reader, out, errOut io.Writer, key, fallbackKey string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var request inference.Request
	if err := json.NewDecoder(io.LimitReader(in, 32<<20)).Decode(&request); err != nil {
		fmt.Fprintln(errOut, "expected a generate request")
		return 2
	}
	result, err := inference.Generate(ctx, request, key, fallbackKey)
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
