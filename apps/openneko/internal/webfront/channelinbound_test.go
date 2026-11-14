package webfront

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestChannelInboundMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/channelinbound.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Parse []struct {
			Channel string            `json:"channel"`
			Raw     json.RawMessage   `json:"raw"`
			Env     map[string]string `json:"env"`
			Parsed  json.RawMessage   `json:"parsed"`
			Error   string            `json:"error"`
		} `json:"parse"`
		Verify []struct {
			Channel string            `json:"channel"`
			Env     map[string]string `json:"env"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
			Now     int64             `json:"now"`
			OK      bool              `json:"ok"`
		} `json:"verify"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Parse) < 40 || len(fixture.Verify) < 13 {
		t.Fatalf("fixture too small: %d parse, %d verify", len(fixture.Parse), len(fixture.Verify))
	}
	for i, c := range fixture.Parse {
		value, err := jsParse(c.Raw)
		if err != nil {
			t.Fatal(err)
		}
		got, err := channelInbound[c.Channel].parse(value, c.Env)
		if c.Error != "" {
			if err == nil || err.Error() != c.Error {
				t.Errorf("%d %s %s: error %v, want %q", i, c.Channel, c.Raw, err, c.Error)
			}
			continue
		}
		want, _ := jsParse(c.Parsed)
		if err != nil || jsStringify(got.js(), false) != jsStringify(want, false) {
			t.Errorf("%d %s %s:\n got %s %v\nwant %s", i, c.Channel, c.Raw, jsStringify(got.js(), false), err, jsStringify(want, false))
		}
	}
	old := slackNow
	t.Cleanup(func() { slackNow = old })
	for i, c := range fixture.Verify {
		slackNow = func() int64 { return c.Now }
		h := http.Header{}
		for k, v := range c.Headers {
			h.Set(k, v)
		}
		if got := channelInbound[c.Channel].verify(h, []byte(c.Body), c.Env); got != c.OK {
			t.Errorf("verify %d %s: %v, want %v", i, c.Channel, got, c.OK)
		}
	}
}

func TestInboundUpdateKeyMatchesNode(t *testing.T) {
	for raw, want := range map[string]string{
		`{"update_id":10,"message":{"text":"hi","chat":{"id":4242}}}`:  "1351aaa14c3d713177f3e9abe4a08f13eb6d28b97e552e03435b72d31420fa09",
		`{"2":2,"10":1,"b":[1,2.5,{"z":null,"a":"é\u2028"}],"a":true}`: "6c355acdc8d2838fe5466dbb4fc64d4bdbf4babb456816af371749d90d907dcd",
		`[{"envelope":1e+21,"small":-0.000001}]`:                       "92b3592cbc865949d69b466c89fb5ae88f48cd60b073372761b8b3c157ca84ca",
	} {
		v, err := jsParse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if got := inboundUpdateKey(v); got != want {
			t.Errorf("%s: %s, want %s", raw, got, want)
		}
	}
}

func TestPollBackoff(t *testing.T) {
	for failures, want := range map[int]time.Duration{0: 3 * time.Second, 1: 3 * time.Second, 2: 6 * time.Second, 5: 48 * time.Second, 6: time.Minute, 40: time.Minute} {
		if got := pollBackoff(failures); got != want {
			t.Errorf("pollBackoff(%d) = %v, want %v", failures, got, want)
		}
	}
	if !logPollFailure(3, true) || logPollFailure(3, false) || !logPollFailure(20, false) {
		t.Error("logPollFailure")
	}
	if got := conversationKey(jsObjOf("channel", "D1"), "1700.1"); got != "D1:1700.1" {
		t.Errorf("slack thread key %q", got)
	}
	if got := conversationKey(jsObjOf("chatId", 4242.0), ""); got != "4242" {
		t.Errorf("telegram key %q", got)
	}
	if got := conversationKey(jsObjOf("to", "+15550001"), ""); got != "+15550001" {
		t.Errorf("address key %q", got)
	}
	if got := conversationKey(jsObjOf("kind", "hud"), "x"); got != "" {
		t.Errorf("no address %q", got)
	}
	if a, b := channelThreadID("org", "telegram", "4242"), channelThreadID("org", "slack", "D1:1700.1"); a != "a78faa74-6b8e-5559-af6d-8af4de3deb88" ||
		b != "6955b271-ea4e-5fcd-a12b-a45948406b43" {
		t.Errorf("thread ids %q %q", a, b)
	}
}
