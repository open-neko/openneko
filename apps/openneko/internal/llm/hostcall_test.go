package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"math"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/catalog"
	"github.com/open-neko/openneko/apps/ax-harness/embedding"
	"github.com/open-neko/openneko/apps/ax-harness/inference"
)

type hostFixture struct {
	Routes []struct {
		Provider string         `json:"provider"`
		Model    string         `json:"model"`
		Config   map[string]any `json:"config"`
		Route    struct {
			Provider string            `json:"provider"`
			Model    string            `json:"model"`
			URL      string            `json:"url"`
			Options  map[string]string `json:"options"`
		} `json:"route"`
	} `json:"routes"`
	Prices []struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Usage    struct {
			Input      int64 `json:"inputTokens"`
			Output     int64 `json:"outputTokens"`
			Total      int64 `json:"totalTokens"`
			CacheRead  int64 `json:"cacheReadTokens"`
			CacheWrite int64 `json:"cacheWriteTokens"`
		} `json:"usage"`
		At    time.Time `json:"at"`
		Price *Price    `json:"price"`
		Cost  *struct {
			Micros  int64  `json:"micros"`
			Source  string `json:"source"`
			Version string `json:"version"`
		} `json:"cost"`
	} `json:"prices"`
}

func TestHostCallsMatchTypeScriptFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../../packages/llm/test/fixtures/host-calls.json")
	if err != nil {
		t.Fatal(err)
	}
	var f hostFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Routes) != len(PrimaryProviders) {
		t.Errorf("the fixture has %d routes for %d providers", len(f.Routes), len(PrimaryProviders))
	}
	for _, c := range f.Routes {
		got, err := AxRoute(Settings{Provider: c.Provider, Model: c.Model, Config: c.Config})
		want := inference.Route{Provider: c.Route.Provider, Model: c.Route.Model, URL: c.Route.URL, Options: c.Route.Options}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %+v %v, want %+v", c.Provider, got, err, want)
		}
	}
	for _, c := range f.Prices {
		u := Usage{Input: c.Usage.Input, Output: c.Usage.Output, Total: c.Usage.Total, CacheRead: c.Usage.CacheRead, CacheWrite: c.Usage.CacheWrite}
		var (
			usd             float64
			source, version string
			ok              = true
		)
		if c.Price != nil {
			usd, source, version = RecordCostUSD(*c.Price, u), "model_record", "model_record"
		} else if usd, source, ok = TokenCostUSD(c.Provider, c.Model, u, c.At); ok {
			version = PricesVersion()
		}
		if c.Cost == nil {
			if ok {
				t.Errorf("%s %s: priced at %v, want no price", c.Provider, c.Model, usd)
			}
			continue
		}
		if catalog.Version() == "" && version == PricesVersion() {
			version = "ax-go@" + axVersion(t) + "+" + PriceOverridesVersion
		}
		micros := int64(math.Max(0, math.Ceil(usd*1_000_000)))
		if !ok || micros != c.Cost.Micros || source != c.Cost.Source || version != c.Cost.Version {
			t.Errorf("%s %s at %s: %d %s %s, want %+v", c.Provider, c.Model, c.At, micros, source, version, *c.Cost)
		}
	}
}

func axVersion(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "github.com/ax-llm/ax/packages/go" {
			return fields[1]
		}
	}
	t.Fatal("go.mod does not pin the Ax module")
	return ""
}

func TestPriceUsageCoverage(t *testing.T) {
	route := inference.Route{Provider: "openai", Model: "gpt-4.1-mini"}
	if u := PriceUsage(inference.Usage{Requests: 1, Coverage: "complete"}, nil, route, time.Now()); u.Coverage != "unavailable" || u.CostStatus != "unknown" || u.CostUSD != nil {
		t.Errorf("no reported usage: %+v", u)
	}
	if u := PriceUsage(inference.Usage{Requests: 1, Reported: 1, InputTokens: 1, TotalTokens: 1, Coverage: "complete"}, nil, inference.Route{Provider: "groq", Model: "none"}, time.Now()); u.CostStatus != "unknown" || u.CostUSD != nil || u.Total != 1 {
		t.Errorf("no price: %+v", u)
	}
	if u := PriceUsage(inference.Usage{Requests: 1, Reported: 1, InputTokens: 1, TotalTokens: 1, Coverage: "partial"}, &Price{0, 0}, route, time.Now()); u.CostStatus != "included" || u.CostSource != "model_record" {
		t.Errorf("a zero price: %+v", u)
	}
}

func fakeServe(t *testing.T, answer func(frame map[string]any) []string) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "serve.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil {
					return
				}
				var frame map[string]any
				_ = json.Unmarshal(line, &frame)
				for _, out := range answer(frame) {
					_, _ = conn.Write([]byte(out + "\n"))
				}
			}()
		}
	}()
	return socket
}

func TestServeGateway(t *testing.T) {
	var seen map[string]any
	socket := fakeServe(t, func(frame map[string]any) []string {
		seen = frame
		return []string{`{"type":"result","result":{"outputs":{"reply":"READY"},"usage":{"requests":1,"reported":1,"input_tokens":3,"output_tokens":1,"total_tokens":4,"coverage":"complete"},"provider":"openai","model":"m","fallback":true}}`, `{"type":"exit","code":0}`}
	})
	request := inference.Request{Route: inference.Route{Provider: "openai", Model: "m"}, Signature: "a:string -> b:string", Values: map[string]any{"a": "x"}}
	got, err := ServeGateway(socket)(context.Background(), request, "k1", "k2")
	if err != nil || got.Outputs["reply"] != "READY" || got.Usage.TotalTokens != 4 || !got.Fallback {
		t.Fatalf("result %+v %v", got, err)
	}
	if seen["type"] != "generate" || seen["model_key"] != "k1" || seen["fallback_key"] != "k2" || seen["generate"].(map[string]any)["signature"] != "a:string -> b:string" {
		t.Errorf("frame %v", seen)
	}
	if strings.Contains(seen["generate"].(map[string]any)["route"].(map[string]any)["model"].(string), "k1") {
		t.Errorf("the key is in the request")
	}

	failing := fakeServe(t, func(map[string]any) []string { return []string{`{"type":"exit","code":1,"error":"provider said no"}`} })
	if _, err := ServeGateway(failing)(context.Background(), request, "k", ""); err == nil || err.Error() != "provider said no" {
		t.Errorf("exit error: %v", err)
	}
	silent := fakeServe(t, func(map[string]any) []string { return []string{`{"type":"exit","code":0}`} })
	if _, err := ServeGateway(silent)(context.Background(), request, "k", ""); err == nil || err.Error() != "openneko serve returned no model result" {
		t.Errorf("no result: %v", err)
	}
	if _, err := ServeGateway(filepath.Join(t.TempDir(), "absent.sock"))(context.Background(), request, "k", ""); err == nil || !strings.HasPrefix(err.Error(), "openneko serve unavailable") {
		t.Errorf("no serve: %v", err)
	}
	var embed map[string]any
	embedSocket := fakeServe(t, func(frame map[string]any) []string {
		embed = frame
		return []string{`{"type":"result","result":{"vectors":[[1,2]],"model":"e"}}`, `{"type":"exit","code":0}`}
	})
	if got, err := ServeEmbedder(embedSocket)(context.Background(), embeddingRequest(), "ek"); err != nil || len(got.Vectors[0]) != 2 || embed["type"] != "embed" || embed["model_key"] != "ek" {
		t.Errorf("embed %+v %v %v", got, err, embed)
	}
}

func TestGatewayKey(t *testing.T) {
	token := func(context.Context) (string, error) { return "adc", nil }
	for _, c := range []struct {
		s    Settings
		want string
	}{
		{Settings{Provider: "vertex", Secrets: map[string]string{"apiKey": "ignored"}}, "adc"},
		{Settings{Provider: "openai", Secrets: map[string]string{"apiKey": "sk"}}, "sk"},
		{Settings{Provider: "ollama", Secrets: map[string]string{}}, "none"},
	} {
		if got, err := GatewayKey(context.Background(), c.s, token); err != nil || got != c.want {
			t.Errorf("%s: %q %v", c.s.Provider, got, err)
		}
	}
}

func embeddingRequest() embedding.Request {
	return embedding.Request{URL: "https://e.example.test/v1", Model: "e", Input: []string{"x"}}
}

func TestProviderTestSignatureParses(t *testing.T) {
	if err := inference.CheckSignature(providerTest.Signature); err != nil {
		t.Fatal(err)
	}
}
