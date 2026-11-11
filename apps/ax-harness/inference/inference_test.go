package inference

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func chatServer(t *testing.T, status int, content string, seen *[]map[string]any, auth *[]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if seen != nil {
			*seen = append(*seen, body)
		}
		if auth != nil {
			*auth = append(*auth, r.Header.Get("Authorization")+r.Header.Get("x-api-key"))
		}
		if status != http.StatusOK {
			http.Error(w, content, status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/messages") {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "model": body["model"],
				"content": []any{map[string]any{"type": "text", "text": content}}, "stop_reason": "end_turn",
				"usage": map[string]any{"input_tokens": 9, "output_tokens": 3}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 4, "total_tokens": 15}})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestGenerateRunsProgramAtAnyOpenAICompatibleURL(t *testing.T) {
	var seen []map[string]any
	var auth []string
	server := chatServer(t, http.StatusOK, "Answer: 42", &seen, &auth)
	result, err := Generate(context.Background(), Request{
		Route:       Route{URL: server.URL, Model: "local-model"},
		Signature:   `question:string "what to answer" -> answer:string "the answer"`,
		Instruction: "Answer the question.",
		Values:      map[string]any{"question": "six times seven"},
	}, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["answer"] != "42" || result.Provider != OpenAICompatible || result.Model != "local-model" {
		t.Fatalf("result = %+v", result)
	}
	if result.Usage.Requests != 1 || result.Usage.InputTokens != 11 || result.Usage.OutputTokens != 4 || result.Usage.Coverage != "complete" {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if len(seen) != 1 || seen[0]["model"] != "local-model" || auth[0] != "Bearer key-1" {
		t.Fatalf("request model=%v auth=%v", seen, auth)
	}
	if raw, _ := json.Marshal(seen[0]); !strings.Contains(string(raw), "six times seven") || !strings.Contains(string(raw), "Answer the question.") {
		t.Fatalf("request lost the input or the instruction: %s", raw)
	}
}

func TestGenerateKeepsTheKeyOutOfErrors(t *testing.T) {
	const key = "sk-planted-4bd1e2"
	server := chatServer(t, http.StatusUnauthorized, "bad key "+key, nil, nil)
	_, err := Generate(context.Background(), Request{
		Route: Route{URL: server.URL, Model: "m"}, Signature: "question:string -> answer:string", Values: map[string]any{"question": "q"},
	}, key)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("error carries the key: %v", err)
	}
}

func TestAnthropicRouteSendsNoTemperature(t *testing.T) {
	var seen []map[string]any
	server := chatServer(t, http.StatusOK, "Reply: READY", &seen, nil)
	result, err := Generate(context.Background(), Request{
		Route: Route{Provider: "anthropic", URL: server.URL, Model: "claude-sonnet-5-5"}, Signature: "prompt:string -> reply:string",
		Values: map[string]any{"prompt": "Reply with READY only."},
	}, "key")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["reply"] != "READY" || len(seen) != 1 {
		t.Fatalf("result = %+v, requests = %d", result, len(seen))
	}
	if _, ok := seen[0]["temperature"]; ok {
		t.Fatalf("anthropic request carries a temperature: %v", seen[0]["temperature"])
	}
}

func TestGenerateRefusesBadRequests(t *testing.T) {
	cases := map[string]Request{
		"compatible without URL": {Route: Route{Model: "m"}, Signature: "question:string -> answer:string"},
		"URL with credentials":   {Route: Route{URL: "https://user:pw@example.test/v1", Model: "m"}, Signature: "question:string -> answer:string"},
		"reserved option":        {Route: Route{Provider: "openai", Model: "m", Options: map[string]string{"base_url": "https://evil.test"}}, Signature: "question:string -> answer:string"},
		"no model":               {Route: Route{Provider: "openai"}, Signature: "question:string -> answer:string"},
		"bad signature":          {Route: Route{Provider: "openai", Model: "m"}, Signature: "question:string ->"},
		"unknown provider":       {Route: Route{Provider: "no-such-provider", Model: "m"}, Signature: "question:string -> answer:string"},
	}
	for name, request := range cases {
		if _, err := Generate(context.Background(), request, "key"); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestEveryAdminProviderRouteBuildsAClient(t *testing.T) {
	routes := []Route{
		{Provider: "openai", Model: "m"},
		{Provider: "anthropic", Model: "m"},
		{Provider: "google-gemini", Model: "m"},
		{Provider: "azure-openai", Model: "d", Options: map[string]string{"resource_name": "r", "deployment_name": "d", "api_version": "2024-10-21"}},
		{Provider: "mistral", Model: "m", URL: "https://api.mistral.ai/v1"},
		{Provider: "groq", Model: "m", URL: "https://api.groq.com/openai/v1"},
		{Provider: "cohere", Model: "m", URL: "https://api.cohere.ai/compatibility/v1"},
		{Provider: "together", Model: "m", URL: "https://api.together.ai/v1"},
		{Provider: "deepseek", Model: "m", URL: "https://api.deepseek.com/v1"},
		{Provider: "huggingface-router", Model: "m", URL: "https://router.huggingface.co/v1"},
		{Provider: "openrouter", Model: "m", URL: "https://openrouter.ai/api/v1"},
		{Provider: "reka", Model: "m", URL: "https://api.reka.ai/v1"},
		{Provider: "grok", Model: "m", URL: "https://api.x.ai/v1"},
		{Provider: "vertex-ai", Model: "m", URL: "https://aiplatform.googleapis.com/v1beta1/projects/p/locations/global/endpoints/openapi"},
		{Model: "m", URL: "http://host.docker.internal:11434/v1"},
	}
	for _, route := range routes {
		if err := route.Validate(); err != nil {
			t.Errorf("%s: %v", route.ProviderName(), err)
			continue
		}
		if _, err := NewClient(route, "key", HostRetry); err != nil {
			t.Errorf("%s: %v", route.ProviderName(), err)
		}
	}
}

var hostProgramCall = regexp.MustCompile(`\bhostProgram\s*\(\s*`)

// readLiteral decodes the static string literal at the start of source.
func readLiteral(source string) (string, bool) {
	if source == "" {
		return "", false
	}
	quote := source[0]
	if quote != '"' && quote != '\'' && quote != '`' {
		return "", false
	}
	escaped := false
	for i := 1; i < len(source); i++ {
		switch {
		case escaped:
			escaped = false
		case source[i] == '\\':
			escaped = true
		case source[i] == quote:
			raw := source[:i+1]
			if quote == '`' {
				if strings.Contains(raw, "${") {
					return "", false
				}
				return strings.NewReplacer("\\`", "`", "\\\\", "\\").Replace(raw[1 : len(raw)-1]), true
			}
			if quote == '\'' {
				raw = `"` + strings.ReplaceAll(raw[1:len(raw)-1], `"`, `\"`) + `"`
			}
			value, err := strconv.Unquote(raw)
			return value, err == nil
		}
	}
	return "", false
}

var genericFieldNames = map[string]bool{"query": true, "response": true}

func TestEveryHostProgramSignatureParses(t *testing.T) {
	root := filepath.Join("..", "..", "..", "packages", "llm", "src")
	found := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".ts") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		source := string(raw)
		for _, match := range hostProgramCall.FindAllStringIndex(source, -1) {
			line := strings.Count(source[:match[0]], "\n") + 1
			if strings.Contains(source[max(0, match[0]-16):match[0]], "function ") {
				continue
			}
			signature, ok := readLiteral(source[match[1]:])
			if !ok {
				t.Errorf("%s:%d: hostProgram needs a static signature literal", path, line)
				continue
			}
			found++
			if err := CheckSignature(signature); err != nil {
				t.Errorf("%s:%d: %v", path, line, err)
				continue
			}
			parsed := parseSignature(signature)
			for _, field := range append(parsed.GetInputFields(), parsed.GetOutputFields()...) {
				if genericFieldNames[strings.ToLower(field.Name)] {
					t.Errorf("%s:%d: field name %q is too generic", path, line, field.Name)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found < 8 {
		t.Fatalf("found %d host programs, want at least 8", found)
	}
}
