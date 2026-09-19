// Deterministic external-model fixture; GraphJin and database execution remain real.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

func main() {
	var mu sync.Mutex
	counts := map[string]int{}
	delay := 0
	http.HandleFunc("/control", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			mu.Lock()
			defer mu.Unlock()
			_ = json.NewEncoder(w).Encode(counts)
			return
		}
		if r.Method != "POST" {
			http.Error(w, "POST required", 405)
			return
		}
		var c struct {
			Delay int `json:"delay"`
		}
		if json.NewDecoder(r.Body).Decode(&c) != nil || c.Delay < 0 || c.Delay > 30 {
			http.Error(w, "invalid", 400)
			return
		}
		mu.Lock()
		counts = map[string]int{}
		delay = c.Delay
		mu.Unlock()
		w.WriteHeader(204)
	})
	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string          `json:"model"`
			Messages json.RawMessage `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		mu.Lock()
		wait := delay
		n := counts[req.Model]
		counts[req.Model]++
		mu.Unlock()
		if wait > 0 {
			select {
			case <-time.After(time.Duration(wait) * time.Second):
			case <-r.Context().Done():
				return
			}
		}
		refused := strings.Contains(string(req.Messages), "not configured read-only") && !strings.Contains(string(req.Messages), "trace_id")
		if n == 2 && !refused && (!strings.Contains(string(req.Messages), "REF-42") || (req.Model != "graphjin-fixture" && !strings.Contains(string(req.Messages), "trace_id"))) {
			http.Error(w, "missing real lookup evidence", 422)
			return
		}
		var responses []string
		if req.Model == "graphjin-fixture" {
			responses = []string{
				`{"javascriptCode":"const schema=query_catalog({id:'table:default:public.references'}); console.log(schema);"}`,
				`{"javascriptCode":"const evidence=execute_graphql({query:'query { references { id label } }'}); final({status:'answered',answer:'The reference is REF-42.',data:evidence.data},{evidence});"}`,
				`{"status":"answered","answer":"The reference is REF-42.","data":{"references":[{"id":42,"label":"REF-42"}]},"evidence":[],"actions":[],"next":[]}`,
			}
		} else {
			responses = []string{`{"javascriptCode":"final('Find the seeded reference', {})"}`, `{"javascriptCode":"const evidence=lookup('Find the seeded reference'); final('Report the reference', {evidence});"}`, `{"answer":"The reference is REF-42."}`}
		}
		if n == 2 && refused {
			responses[n] = `{"answer":"The lookup was refused because the data agent is not configured read-only."}`
		}
		if n >= len(responses) {
			http.Error(w, "fixture exhausted", 400)
			return
		}
		fmt.Printf("model=%s step=%d\n", req.Model, n)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": responses[n]}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 10, "total_tokens": 20}})
	})
	panic(http.ListenAndServe(":8080", nil))
}
