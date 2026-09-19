// The M2 probe qualifies real OpenShell transport. It is not the product backend.
package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	ax "github.com/ax-llm/ax/packages/go"
)

func main() {
	serve := flag.Bool("serve", false, "serve the synthetic model endpoint")
	deny := flag.Bool("deny", false, "expect destination-bound credential rejection")
	base := flag.String("url", "http://model-fixture:8080", "model fixture origin")
	cancelStream := flag.Bool("cancel", false, "cancel after first event")
	untrusted := flag.Bool("untrusted", false, "expect missing interception CA")
	oauth := flag.Bool("oauth-serve", false, "serve synthetic OAuth on gateway loopback")
	query := flag.Bool("query", false, "exercise query credential replacement")
	revoked := flag.Bool("revoked", false, "expect revoked binding rejection")
	configure := flag.String("configure-key", "", "set synthetic fixture credential")
	flag.Parse()
	if *oauth {
		if err := http.ListenAndServe("127.0.0.1:18081", http.HandlerFunc(oauthFixture)); err != nil {
			panic(err)
		}
		return
	}
	if *configure != "" {
		if err := configureKey(*configure); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *query {
		if err := queryProbe(*base, os.Getenv("MODEL_API_KEY")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *serve {
		http.HandleFunc("/", fixture)
		http.HandleFunc("/control", control)
		go func() {
			if err := http.ListenAndServeTLS(":8443", "/tls/fixture.crt", "/tls/fixture.key", nil); err != nil {
				panic(err)
			}
		}()
		if err := http.ListenAndServe(":8080", nil); err != nil {
			panic(err)
		}
		return
	}
	if err := probe(*base, *deny, *cancelStream, *untrusted, *revoked); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func probe(origin string, deny, cancelStream, untrusted, revoked bool) error {
	for _, value := range os.Environ() {
		if strings.Contains(value, "synthetic-M2-") || strings.Contains(value, "synthetic-refresh-secret") {
			return fmt.Errorf("real synthetic credential exposed in workload environment")
		}
	}
	key := os.Getenv("MODEL_API_KEY")
	if !strings.HasPrefix(key, "openshell:resolve:") {
		return fmt.Errorf("workload did not receive a credential placeholder")
	}
	base := origin + "/v1"
	if deny {
		base = origin + "/forbidden"
	}
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", base, "api_key", key, "model", "fixture"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	prompt := "probe"
	if cancelStream {
		prompt = "cancel-probe"
	}
	stream, err := client.StreamEvents(ctx, ax.Object("chat_prompt", ax.Array(ax.Object("role", "user", "content", prompt))), nil)
	if untrusted {
		if err == nil {
			stream.Close()
			return fmt.Errorf("untrusted interception certificate accepted")
		}
		var unknown x509.UnknownAuthorityError
		if !errors.As(err, &unknown) && !strings.Contains(err.Error(), "certificate signed by unknown authority") {
			return fmt.Errorf("expected unknown CA error, got %w", err)
		}
		fmt.Println(`{"check":"missing_interception_ca_rejected","ok":true}`)
		return nil
	}
	if deny || revoked {
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
		if cancelStream {
			start := time.Now()
			cancel()
			if stream.Next() {
				return fmt.Errorf("stream continued after cancellation")
			}
			if stream.Err() == nil {
				return fmt.Errorf("cancelled stream returned success")
			}
			if time.Since(start) > 2*time.Second {
				return fmt.Errorf("stream cancellation exceeded two seconds")
			}
			fmt.Println(`{"check":"sandbox_stream_cancelled","ok":true}`)
			return nil
		}
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
	credentialState.RLock()
	expected := credentialState.key
	credentialState.RUnlock()
	if r.URL.Path == "/v1/query" {
		if r.URL.Query().Get("key") != expected {
			http.Error(w, "query substitution failed", 401)
			return
		}
		fmt.Fprint(w, "verified")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/alternate/") {
		expected = "synthetic-M2-alternate"
	}

	var body struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "invalid body", 400)
		return
	}
	if r.URL.Path != "/v1/chat/completions" && r.URL.Path != "/alternate/v1/chat/completions" {
		http.Error(w, "wrong route", 400)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+expected {
		http.Error(w, "credential substitution failed", 401)
		return
	}
	fmt.Println(`{"check":"upstream_auth_verified","ok":true}`)
	w.Header().Set("Content-Type", "text/event-stream")
	var finish ax.Value = "stop"
	if len(body.Messages) > 0 && body.Messages[0].Content == "cancel-probe" {
		finish = nil
	}
	event := ax.Object("id", "probe", "choices", ax.Array(ax.Object("index", 0, "delta", ax.Object("content", "verified"), "finish_reason", finish)))
	payload, _ := json.Marshal(event)
	fmt.Fprintf(w, "data: %s\n\n", payload)
	w.(http.Flusher).Flush()
	if len(body.Messages) > 0 && body.Messages[0].Content == "cancel-probe" {
		select {
		case <-r.Context().Done():
			fmt.Println(`{"check":"upstream_stream_cancelled","ok":true}`)
		case <-time.After(10 * time.Second):
			fmt.Println(`{"check":"upstream_stream_cancelled","ok":false}`)
		}
		return
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	w.(http.Flusher).Flush()
}
