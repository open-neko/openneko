package webfront

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

func TestRunEventScrubMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/runeventscrub.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Values []string `json:"values"`
		Cases  []struct {
			Event    json.RawMessage `json:"event"`
			Scrubbed string          `json:"scrubbed"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Cases) < 21 {
		t.Fatalf("%d cases", len(fx.Cases))
	}
	scrub := secretScrubber(fx.Values)
	for _, c := range fx.Cases {
		event, err := jsParse(c.Event)
		if err != nil {
			t.Fatal(err)
		}
		if got := jsStringify(scrubRunEvent(scrub, event), false); got != c.Scrubbed {
			t.Errorf("%s\n got %s\nwant %s", c.Event, got, c.Scrubbed)
		}
	}
	event := jsObjOf("type", "tool_end", "result", "x")
	if scrubRunEvent(nil, event) != event {
		t.Error("no secrets: the event changed")
	}
	if got := jsStringify(scrubRunEvent(scrub, map[string]any{"type": "tool_end", "result": "sk-live-0123456789abcdef"}), false); got != `{"result":"[REDACTED]","type":"tool_end"}` {
		t.Errorf("a Go map event: %s", got)
	}
}

func TestRunEventScrubberReloads(t *testing.T) {
	a := &API{SecretsDir: t.TempDir()}
	if a.runEventScrubber() != nil {
		t.Fatal("no secrets file: a scrubber was built")
	}
	write := func(store secrets.Store, mtime time.Time) {
		t.Helper()
		if err := secrets.Write(store, a.SecretsDir); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(secrets.Path(a.SecretsDir), mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	write(secrets.Store{"pack.shop": {"API_KEY": "short"}}, now)
	if a.runEventScrubber() != nil {
		t.Fatal("only short values: a scrubber was built")
	}
	write(secrets.Store{"pack.shop": {"API_KEY": "first-planted-key"}}, now.Add(time.Second))
	if got := a.runEventScrubber()("a first-planted-key b"); got != "a [REDACTED] b" {
		t.Fatalf("first %q", got)
	}
	write(secrets.Store{"channel.slack": {"BOT_TOKEN": "second-planted-key"}}, now.Add(2*time.Second))
	scrub := a.runEventScrubber()
	if got := scrub("first-planted-key second-planted-key"); got != "first-planted-key [REDACTED]" {
		t.Fatalf("after the change %q", got)
	}
	if err := os.WriteFile(secrets.Path(a.SecretsDir), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := a.runEventScrubber()("second-planted-key"); got != "[REDACTED]" {
		t.Fatalf("an unreadable file dropped the last scrubber: %q", got)
	}
}
