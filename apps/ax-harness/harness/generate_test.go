package harness

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGenerateModeAnswersOneHostCall(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "Answer: 42"}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	in := strings.NewReader(`{"route":{"url":"` + server.URL + `","model":"m"},"signature":"question:string -> answer:string","values":{"question":"q"}}`)
	var out, errOut bytes.Buffer
	if code := generateMain(in, &out, &errOut, "env-key", ""); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	var result struct {
		Outputs map[string]any `json:"outputs"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Outputs["answer"] != "42" || auth != "Bearer env-key" {
		t.Fatalf("out = %s, auth = %q", out.String(), auth)
	}
	if code := generateMain(strings.NewReader("not json"), &out, &errOut, "", ""); code != 2 {
		t.Fatalf("bad request code = %d", code)
	}
}
