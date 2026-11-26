package webfront

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestGraphQLOperationKinds(t *testing.T) {
	for doc, want := range map[string]string{
		`{ racks { id } }`:                                                    "query",
		`query Q($id: ID) { racks(id: $id) { id } }`:                          "query",
		`subscription { racks { id } }`:                                       "subscription",
		`mutation { racks(insert: {id: 1}) { id } }`:                          "mutation",
		"# a note\nquery { a { b } }":                                         "query",
		`query { a(where: { name: { eq: "mutation { x }" } }) { b } }`:        "query",
		`query A { a { ...F } } fragment F on a { b } mutation B { c { d } }`: "query,mutation",
	} {
		kinds, err := graphqlOperationKinds(doc)
		if err != nil || strings.Join(kinds, ",") != want {
			t.Errorf("%q: %v %v, want %s", doc, kinds, err, want)
		}
	}
	for _, bad := range []string{`query { a { b }`, `drop table x`, `query { a(x: "open) { b } }`} {
		if _, err := graphqlOperationKinds(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
	if worldsReadOnly(`subscription { a { b } }`, false) == nil || worldsReadOnly(`subscription { a { b } }`, true) != nil {
		t.Error("a subscription passes only on the socket")
	}
	if worldsReadOnly(`query A { a { b } } mutation B { c { d } }`, true) == nil {
		t.Error("a mutation behind a query passed")
	}
}

func TestWorldsSocketMessage(t *testing.T) {
	out, refusal := worldsSocketMessage([]byte(`{"type":"connection_init","payload":{"Authorization":"Bearer someone-else"}}`), "the-token")
	if refusal != nil || !strings.Contains(string(out), "Bearer the-token") || strings.Contains(string(out), "someone-else") {
		t.Errorf("init: %s %s", out, refusal)
	}
	if _, refusal := worldsSocketMessage([]byte(`{"type":"subscribe","id":"1","payload":{"query":"mutation { a { b } }"}}`), ""); !strings.Contains(string(refusal), `"id":"1"`) {
		t.Errorf("a mutation was not refused: %s", refusal)
	}
	if out, refusal := worldsSocketMessage([]byte(`{"type":"subscribe","id":"1","payload":{"query":"subscription { a { b } }"}}`), ""); refusal != nil || out == nil {
		t.Errorf("a subscription was refused: %s", refusal)
	}
}

// fakeGraphJin answers each read with the Authorization header that it got, so
// a test sees who read. Its socket echoes the first message and each subscribe.
type fakeGraphJin struct {
	mu      sync.Mutex
	queries []string
	auth    []string
}

func (g *fakeGraphJin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") == "websocket" {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"graphql-transport-ws"}})
		if err != nil {
			return
		}
		defer c.CloseNow() //nolint:errcheck
		for {
			_, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			var msg map[string]any
			_ = json.Unmarshal(data, &msg)
			switch msg["type"] {
			case "connection_init":
				payload, _ := msg["payload"].(map[string]any)
				g.mu.Lock()
				g.auth = append(g.auth, asString(payload["Authorization"]))
				g.mu.Unlock()
				_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"connection_ack"}`))
			case "subscribe":
				reply, _ := json.Marshal(map[string]any{"type": "next", "id": msg["id"], "payload": map[string]any{"data": map[string]any{"racks": []any{map[string]any{"id": "R1"}}}}})
				_ = c.Write(r.Context(), websocket.MessageText, reply)
			}
		}
	}
	body, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	g.queries = append(g.queries, string(body))
	g.auth = append(g.auth, r.Header.Get("Authorization"))
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"data":{"racks":[{"id":"R1"}]}}`))
}

// asString is a value as a string, or "" when it is not one.
func asString(v any) string {
	s, _ := v.(string)
	return s
}

// claimsOf reads the payload of a GraphJin token, without its signature.
func claimsOf(t *testing.T, header string) map[string]any {
	t.Helper()
	parts := strings.Split(strings.TrimPrefix(header, "Bearer "), ".")
	if len(parts) != 3 {
		t.Fatalf("not a token: %q", header)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	_ = json.Unmarshal(raw, &claims)
	return claims
}

func TestWorldsGraphQLReadsAsThePerson(t *testing.T) {
	f, _, db := signinFront(t, liveScalekit)
	if err := os.WriteFile(filepath.Join(f.API.SecretsDir, "secret-key"), []byte("worlds-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	admin, member := sessionCookie("admin"), sessionCookie("member")
	const path = "/api/v1/worlds/graphql"

	if w := call(f, "POST", path, `{"query":"{ racks { id } }"}`, member); w.Code != 404 {
		t.Fatalf("no data source: %d %s", w.Code, w.Body.String())
	}
	g := &fakeGraphJin{}
	gj := httptest.NewServer(g)
	defer gj.Close()
	seed(t, db, `insert into data_source (org_id, kind, name, graphql_url, auth_mode, is_default, enabled) values
		('org', 'graphjin', 'plant', $1, 'jwt', true, true)`, gj.URL)

	if w := call(f, "POST", path, `{"query":"{ racks { id } }"}`, nil); w.Code == 200 || len(g.queries) != 0 {
		t.Errorf("a read with no sign-in: %d", w.Code)
	}
	w := call(f, "POST", path, `{"query":"{ racks { id } }"}`, member)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "R1") {
		t.Fatalf("member read: %d %s", w.Code, w.Body.String())
	}
	if claims := claimsOf(t, g.auth[0]); claims["role"] != "member" || claims["sub"] != "member" || claims["org_id"] != "org" {
		t.Errorf("member claims %v", claims)
	}
	if w := call(f, "POST", path, `{"query":"{ racks { id } }"}`, admin); w.Code != 200 {
		t.Fatalf("admin read: %d", w.Code)
	}
	if claims := claimsOf(t, g.auth[1]); claims["role"] != "admin" {
		t.Errorf("admin claims %v", claims)
	}
	for name, body := range map[string]string{
		"a mutation":     `{"query":"mutation { racks(insert: {id: \"R9\"}) { id } }"}`,
		"a hidden write": `{"query":"query A { racks { id } } mutation B { racks(delete: true) { id } }"}`,
		"a subscription": `{"query":"subscription { racks { id } }"}`,
	} {
		if w := call(f, "POST", path, body, member); w.Code != 403 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if w := call(f, "POST", path, `{"query":"`+strings.Repeat("x", worldsGraphQLLimit)+`"}`, member); w.Code != 413 {
		t.Errorf("a large read: %d", w.Code)
	}
	if len(g.queries) != 2 {
		t.Errorf("a refused read reached GraphJin: %d reads", len(g.queries))
	}

	// The socket: the person's token replaces the builder's own first message, and a subscription passes.
	front := httptest.NewServer(f)
	defer front.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	header := http.Header{}
	header.Set("Cookie", member["Cookie"])
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(front.URL, "http")+path, &websocket.DialOptions{Subprotocols: []string{"graphql-transport-ws"}, HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow() //nolint:errcheck
	write := func(text string) {
		if err := c.Write(ctx, websocket.MessageText, []byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	read := func() string {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	write(`{"type":"connection_init","payload":{"Authorization":"Bearer forged"}}`)
	if got := read(); !strings.Contains(got, "connection_ack") {
		t.Fatalf("no ack: %s", got)
	}
	write(`{"type":"subscribe","id":"2","payload":{"query":"mutation { racks(delete: true) { id } }"}}`)
	if got := read(); !strings.Contains(got, `"error"`) {
		t.Errorf("a mutation on the socket: %s", got)
	}
	write(`{"type":"subscribe","id":"1","payload":{"query":"subscription { racks { id } }"}}`)
	if got := read(); !strings.Contains(got, `"next"`) || !strings.Contains(got, "R1") {
		t.Errorf("subscription: %s", got)
	}
	g.mu.Lock()
	last := g.auth[len(g.auth)-1]
	g.mu.Unlock()
	if strings.Contains(last, "forged") || claimsOf(t, last)["sub"] != "member" {
		t.Errorf("the socket read as %q", last)
	}
}
