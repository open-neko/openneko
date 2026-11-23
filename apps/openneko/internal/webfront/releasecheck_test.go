package webfront

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDatasourceQueryCheckSignsAsTheOrgService(t *testing.T) {
	secrets := t.TempDir()
	if err := os.WriteFile(filepath.Join(secrets, "secret-key"), []byte("release-check-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reply := `{"data":{"soh":[{"id":7}]}}`
	isError := false
	var gotAuth, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("authorization")
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			ID     any `json:"id"`
			Params struct {
				Name      string `json:"name"`
				Arguments struct {
					Query string `json:"query"`
				} `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(raw, &req)
		gotQuery = req.Params.Arguments.Query
		text, _ := json.Marshal(reply)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(mustJSON(req.ID)) + `,"result":{"content":[{"type":"text","text":` + string(text) + `}],"isError":` + map[bool]string{true: "true", false: "false"}[isError] + `}}`))
	}))
	defer srv.Close()
	ctx := context.Background()
	query := "query { soh(limit: 1, order_by: { id: desc }) { id } }"

	if err := DatasourceQueryCheck(ctx, secrets, srv.URL, "adventureworks", query); err != nil {
		t.Fatal(err)
	}
	if gotQuery != query {
		t.Fatalf("query = %q", gotQuery)
	}
	claims := (&API{SecretsDir: secrets}).verifyGraphjinToken(strings.TrimPrefix(gotAuth, "Bearer "), "adventureworks")
	if claims == nil || claims["role"] != "service" || claims["org_id"] != "adventureworks" {
		t.Fatalf("token claims = %v (header %q)", claims, gotAuth)
	}
	if (&API{SecretsDir: secrets}).verifyGraphjinToken(strings.TrimPrefix(gotAuth, "Bearer "), "other") != nil {
		t.Fatal("the token verifies for another org")
	}

	for name, c := range map[string]struct {
		reply   string
		isError bool
	}{
		"no rows":         {`{"data":{"soh":[]}}`, false},
		"not JSON":        {`denied`, false},
		"error":           {`{"data":{"soh":[{"id":7}]}}`, true},
		"no data":         {`{"errors":[{"message":"x"}]}`, false},
		"one field empty": {`{"data":{"soh":[{"id":7}],"customer":[]}}`, false},
		"one field null":  {`{"data":{"soh":[{"id":7}],"customer":null}}`, false},
		"object field":    {`{"data":{"soh":[{"id":7}],"customer":{"id":1}}}`, false},
		"empty data":      {`{"data":{}}`, false},
	} {
		reply, isError = c.reply, c.isError
		if err := DatasourceQueryCheck(ctx, secrets, srv.URL, "adventureworks", query); err == nil {
			t.Errorf("%s: the check passed", name)
		}
	}
}
