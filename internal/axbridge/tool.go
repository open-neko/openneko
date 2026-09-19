// Package axbridge contains compatibility bindings for the pinned Ax revision.
package axbridge

import (
	"context"

	ax "github.com/ax-llm/ax/packages/go"
)

// BindTool binds a tool to one run. Never share the returned tool across runs.
// Ax's synchronous invocation path uses context.Background for ContextHandler;
// clearing it selects Ax's run-scoped Handler wrapper, which preserves tool spans.
// The captured context supplies cancellation to the actual host operation.
// Remove this adapter when an upstream revision passes our HTTP contract tests.
func BindTool(ctx context.Context, tool ax.Tool, handler func(context.Context, map[string]ax.Value) (ax.Value, error)) ax.Tool {
	if ctx == nil || handler == nil {
		panic("run context and tool handler are required")
	}
	tool.ContextHandler = nil
	tool.Handler = func(args map[string]ax.Value) (ax.Value, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return handler(ctx, args)
	}
	return tool
}
