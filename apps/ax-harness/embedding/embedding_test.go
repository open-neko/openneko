package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func service(t *testing.T, handle func(w http.ResponseWriter, body map[string]any, auth string)) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		handle(w, body, r.Header.Get("authorization"))
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1/"
}

func TestEmbedReturnsVectorsInInputOrder(t *testing.T) {
	var got map[string]any
	var auth string
	url := service(t, func(w http.ResponseWriter, body map[string]any, a string) {
		got, auth = body, a
		_, _ = w.Write([]byte(`{"data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":7}}`))
	})
	result, err := Embed(context.Background(), Request{URL: url, Model: "m", Dimensions: 2, Input: []string{"a", "b"}}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if result.Vectors[0][0] != 1 || result.Vectors[1][1] != 1 || result.Model != "m" || result.InputTokens != 7 {
		t.Fatalf("result = %+v", result)
	}
	if auth != "Bearer secret" || got["model"] != "m" || got["dimensions"] != float64(2) || got["encoding_format"] != "float" || len(got["input"].([]any)) != 2 {
		t.Fatalf("request = %v, auth = %q", got, auth)
	}
}

func TestEmbedSendsNoKeyOrDimensionsWhenUnset(t *testing.T) {
	url := service(t, func(w http.ResponseWriter, body map[string]any, auth string) {
		if auth != "" || body["dimensions"] != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.5,0.5]}]}`))
	})
	if _, err := Embed(context.Background(), Request{URL: url, Model: "m", Input: []string{"a"}}, ""); err != nil {
		t.Fatal(err)
	}
}

func TestEmbedRefusesAnIncompatibleReply(t *testing.T) {
	for name, reply := range map[string]string{
		"wrong count":      `{"data":[]}`,
		"wrong dimensions": `{"data":[{"embedding":[1,2,3]}]}`,
		"empty vector":     `{"data":[{"embedding":[]}]}`,
		"index twice":      `{"data":[{"index":0,"embedding":[1,2]},{"index":0,"embedding":[1,2]}]}`,
		"index outside":    `{"data":[{"index":5,"embedding":[1,2]}]}`,
		"not JSON":         `<html>`,
	} {
		t.Run(name, func(t *testing.T) {
			url := service(t, func(w http.ResponseWriter, _ map[string]any, _ string) { _, _ = w.Write([]byte(reply)) })
			input := []string{"a"}
			if name == "index twice" {
				input = []string{"a", "b"}
			}
			if _, err := Embed(context.Background(), Request{URL: url, Model: "m", Dimensions: 2, Input: input}, ""); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestEmbedKeepsTheKeyOutOfErrors(t *testing.T) {
	url := service(t, func(w http.ResponseWriter, _ map[string]any, _ string) { w.WriteHeader(http.StatusServiceUnavailable) })
	_, err := Embed(context.Background(), Request{URL: url, Model: "m", Input: []string{"a"}}, "planted-key-0451")
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") || strings.Contains(err.Error(), "planted-key-0451") {
		t.Fatalf("err = %v", err)
	}
	_, err = Embed(context.Background(), Request{URL: "http://127.0.0.1:1/v1", Model: "m", Input: []string{"a"}}, "planted-key-0451")
	if err == nil || strings.Contains(err.Error(), "planted-key-0451") {
		t.Fatalf("err = %v", err)
	}
}

func TestEmbedRefusesARedirect(t *testing.T) {
	target := service(t, func(w http.ResponseWriter, _ map[string]any, _ string) {
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1]}]}`))
	})
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+"embeddings", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	if _, err := Embed(context.Background(), Request{URL: redirect.URL, Model: "m", Input: []string{"a"}}, "k"); err == nil {
		t.Fatal("followed a redirect")
	}
}

func TestEmbedRefusesBadRequests(t *testing.T) {
	long := strings.Repeat("x", MaxInputChars+1)
	many := make([]string, MaxInputs+1)
	for i := range many {
		many[i] = "a"
	}
	for name, request := range map[string]Request{
		"no model":        {URL: "http://e/v1", Input: []string{"a"}},
		"no input":        {URL: "http://e/v1", Model: "m"},
		"blank input":     {URL: "http://e/v1", Model: "m", Input: []string{" "}},
		"long input":      {URL: "http://e/v1", Model: "m", Input: []string{long}},
		"too many":        {URL: "http://e/v1", Model: "m", Input: many},
		"bad scheme":      {URL: "file:///etc", Model: "m", Input: []string{"a"}},
		"credentials":     {URL: "http://u:p@e/v1", Model: "m", Input: []string{"a"}},
		"query":           {URL: "http://e/v1?k=1", Model: "m", Input: []string{"a"}},
		"dimensions high": {URL: "http://e/v1", Model: "m", Dimensions: MaxDimensions + 1, Input: []string{"a"}},
	} {
		if _, err := Embed(context.Background(), request, ""); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
