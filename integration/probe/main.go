// The M2 probe qualifies real OpenShell transport. It is not the product backend.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	ax "github.com/ax-llm/ax/packages/go"
)

func main() {
	serve := flag.Bool("serve", false, "serve the synthetic model endpoint")
	deny := flag.Bool("deny", false, "expect destination-bound credential rejection")
	flag.Parse()
	if *serve {
		http.HandleFunc("/", fixture)
		if err := http.ListenAndServe(":8080", nil); err != nil {
			panic(err)
		}
		return
	}
	if err := probe(*deny); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func probe(deny bool) error {
	key := os.Getenv("MODEL_API_KEY")
	if !strings.HasPrefix(key, "openshell:resolve:") {
		return fmt.Errorf("workload did not receive a credential placeholder")
	}
	base := "http://model-fixture:8080/v1"
	if deny {
		base = "http://model-fixture:8080/forbidden"
	}
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", base, "api_key", key, "model", "fixture"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	stream, err := client.StreamEvents(ctx, ax.Object("chat_prompt", ax.Array(ax.Object("role", "user", "content", "probe"))), nil)
	if deny {
		if err == nil {
			stream.Close()
			return fmt.Errorf("forbidden credential destination was accepted")
		}
		var apiErr ax.AxError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
			return fmt.Errorf("expected credential-policy HTTP 403, got %T", err)
		}
		fmt.Println(`{"check":"destination_rejected","ok":true,"status":403}`)
		return nil
	}
	if err != nil {
		return fmt.Errorf("stream transport failed: %w", err)
	}
	defer stream.Close()
	events := 0
	for stream.Next() {
		events++
	}
	if err = stream.Err(); err != nil {
		return err
	}
	if events == 0 {
		return fmt.Errorf("model returned no stream events")
	}
	fmt.Printf("{\"check\":\"credential_stream\",\"ok\":true,\"events\":%d}\n", events)
	return nil
}
func fixture(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	if r.URL.Path != "/v1/chat/completions" {
		http.Error(w, "wrong route", 400)
		return
	}
	if r.Header.Get("Authorization") != "Bearer synthetic-M2-credential" {
		http.Error(w, "credential substitution failed", 401)
		return
	}
	fmt.Println(`{"check":"upstream_auth_verified","ok":true}`)
	w.Header().Set("Content-Type", "text/event-stream")
	event := ax.Object("id", "probe", "choices", ax.Array(ax.Object("index", 0, "delta", ax.Object("content", "verified"), "finish_reason", "stop")))
	payload, _ := json.Marshal(event)
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
	w.(http.Flusher).Flush()
}
