// harness-batch runs a pinned workflow script outside the model transcript.
// The trusted launcher provides the script bundle and workspace.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/open-neko/harness/adapters/openneko/batchshell"
	"github.com/open-neko/harness/adapters/openneko/broker"
	"github.com/open-neko/harness/internal/batch"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: harness-batch YYYY-MM-DD")
		os.Exit(2)
	}
	query, err := broker.GraphQLQuery(os.Getenv("OPENNEKO_BROKER_URL"), os.Getenv("OPENNEKO_BROKER_TOKEN"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid batch broker binding")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 21*time.Minute)
	defer cancel()
	var columns []string
	if err := json.Unmarshal([]byte(os.Getenv("HARNESS_BATCH_COLUMNS_JSON")), &columns); err != nil || len(columns) == 0 || len(columns) > 64 {
		fmt.Fprintln(os.Stderr, "invalid workflow batch columns")
		os.Exit(2)
	}
	cfg := batch.Config{
		Script: os.Getenv("HARNESS_BATCH_SCRIPT"), ScriptSHA256: os.Getenv("HARNESS_BATCH_SCRIPT_SHA256"),
		WorkDir: os.Getenv("HARNESS_BATCH_WORK_DIR"), ArtifactDir: os.Getenv("HARNESS_BATCH_ARTIFACT_DIR"),
		ArtifactName:   os.Getenv("HARNESS_BATCH_ARTIFACT_NAME"),
		ScriptCacheDir: batchshell.RemoteCacheDir,
		TargetDay:      os.Args[1], Columns: columns, MaxQueries: 128,
	}
	runner, err := batchshell.New(cfg, batchshell.Options{
		CLI: os.Getenv("HARNESS_OPENSHELL_BIN"), Gateway: os.Getenv("OPENSHELL_GATEWAY"),
		Image: os.Getenv("HARNESS_BATCH_IMAGE"), BundleRoot: os.Getenv("HARNESS_BATCH_BUNDLE_DIR"),
		BundleSHA256: os.Getenv("HARNESS_BATCH_BUNDLE_SHA256"), RunID: os.Getenv("HARNESS_BATCH_RUN_ID"),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid batch compartment:", err)
		os.Exit(2)
	}
	result, err := batch.Run(ctx, cfg, query, runner.Step, runner.Close)
	if err != nil {
		fmt.Fprintln(os.Stderr, "batch failed:", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, "batch result unavailable")
		os.Exit(1)
	}
}
