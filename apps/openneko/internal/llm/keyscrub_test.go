package llm

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
)

const oddKey = `sk-a"b\c<d>e&f+g/h=i j`

func scrubDoor(t *testing.T, handler http.HandlerFunc) (*http.Client, *DoorPass) {
	t.Helper()
	door := &KeyDoor{}
	client := doorClient(t, door)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return client, issuePass(t, door, map[string]DoorRoute{"primary": {Upstream: srv.URL, Key: oddKey}}, 10)
}

func callDoor(t *testing.T, client *http.Client, pass *DoorPass) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, keydoor.URL("primary")+"/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+pass.Token)
	req.Header.Set("Accept-Encoding", "gzip, br")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func readAll(t *testing.T, res *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func jsonForm(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw[1 : len(raw)-1])
}

func TestKeyDoorScrubsTheKeyFromA2xxBody(t *testing.T) {
	client, pass := scrubDoor(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"echo":"`+oddKey+`"}`)
	})
	body := readAll(t, callDoor(t, client, pass))
	if strings.Contains(body, oddKey) || !strings.Contains(body, redactMark) {
		t.Errorf("2xx body %q", body)
	}
}

func TestKeyDoorScrubsAKeySplitAcrossTwoChunks(t *testing.T) {
	client, pass := scrubDoor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\ndata: "+oddKey[:7])
		w.(http.Flusher).Flush()
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, oddKey[7:]+"\n\n")
	})
	res := callDoor(t, client, pass)
	first := make([]byte, len("data: first\n\n"))
	if _, err := io.ReadFull(res.Body, first); err != nil || string(first) != "data: first\n\n" {
		t.Fatalf("first event %q, %v", first, err)
	}
	body := string(first) + readAll(t, res)
	if strings.Contains(body, oddKey) || strings.Contains(body, oddKey[:7]) || body != "data: first\n\ndata: "+redactMark+"\n\n" {
		t.Errorf("streamed body %q", body)
	}
}

func TestKeyDoorScrubsTheKeyFromResponseHeaders(t *testing.T) {
	client, pass := scrubDoor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo", "key="+oddKey)
		w.Header().Add("Set-Cookie", "k="+url.QueryEscape(oddKey))
		w.Header().Set("Trailer", "X-Tail")
		_, _ = io.WriteString(w, "{}")
		w.Header().Set("X-Tail", jsonForm(oddKey))
	})
	res := callDoor(t, client, pass)
	_ = readAll(t, res)
	for name, values := range res.Header {
		for _, v := range values {
			if strings.Contains(v, oddKey) || strings.Contains(v, url.QueryEscape(oddKey)) {
				t.Errorf("header %s: %q", name, v)
			}
		}
	}
	if v := res.Trailer.Get("X-Tail"); v != redactMark {
		t.Errorf("trailer X-Tail %q", v)
	}
}

func TestKeyDoorScrubsAGzipErrorBodyAndAsksForIdentity(t *testing.T) {
	gotEncoding := make(chan string, 1)
	client, pass := scrubDoor(t, func(w http.ResponseWriter, r *http.Request) {
		gotEncoding <- r.Header.Get("Accept-Encoding")
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = io.WriteString(zw, `{"error":"bad key `+oddKey+`"}`)
		_ = zw.Close()
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write(buf.Bytes())
	})
	res := callDoor(t, client, pass)
	body := readAll(t, res)
	if res.StatusCode != http.StatusUnauthorized || body != `{"error":"bad key `+redactMark+`"}` {
		t.Errorf("gzip error answer %d %q", res.StatusCode, body)
	}
	if enc := <-gotEncoding; enc != "identity" {
		t.Errorf("the upstream got Accept-Encoding %q, want identity", enc)
	}
}

func TestKeyDoorRefusesAnEncodingItCannotScrub(t *testing.T) {
	client, pass := scrubDoor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "br")
		_, _ = io.WriteString(w, oddKey)
	})
	res := callDoor(t, client, pass)
	if body := readAll(t, res); res.StatusCode != http.StatusBadGateway || strings.Contains(body, oddKey) {
		t.Errorf("br answer %d %q", res.StatusCode, body)
	}
}

func TestKeyDoorScrubsEachEncodedFormOfTheKey(t *testing.T) {
	forms := map[string]string{
		"raw":         oddKey,
		"json":        jsonForm(oddKey),
		"query":       url.QueryEscape(oddKey),
		"path":        url.PathEscape(oddKey),
		"lowerhex":    strings.ToLower(url.QueryEscape(oddKey)),
		"base64 at 0": base64.StdEncoding.EncodeToString([]byte(oddKey)),
		"base64 at 1": base64.StdEncoding.EncodeToString([]byte("x" + oddKey + "tail")),
		"base64 at 2": base64.StdEncoding.EncodeToString([]byte("xy" + oddKey + "tail")),
		"base64url":   base64.URLEncoding.EncodeToString([]byte("Basic:" + oddKey)),
	}
	var plainJSON bytes.Buffer
	enc := json.NewEncoder(&plainJSON)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(oddKey)
	forms["json no html escape"] = strings.Trim(strings.TrimSpace(plainJSON.String()), `"`)
	for name, form := range forms {
		client, pass := scrubDoor(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"v":"`+form+`"}`)
		})
		body := readAll(t, callDoor(t, client, pass))
		if !strings.Contains(body, redactMark) {
			t.Errorf("%s: body %q kept the key", name, body)
		}
		if decoded := decodeEach(body); strings.Contains(decoded, oddKey) || strings.Contains(decoded, jsonForm(oddKey)) {
			t.Errorf("%s: body %q still holds the key", name, body)
		}
	}
}

func decodeEach(body string) string {
	var out strings.Builder
	out.WriteString(body)
	if s, err := url.QueryUnescape(body); err == nil {
		out.WriteString(s)
	}
	for _, part := range strings.Split(body, redactMark) {
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
			if raw, err := enc.DecodeString(strings.TrimSuffix(strings.TrimPrefix(part, `{"v":"`), `"}`)); err == nil {
				out.Write(raw)
			}
		}
	}
	return out.String()
}

func TestScrubReaderRemovesTheKeyAtEveryChunkBorder(t *testing.T) {
	forms := keyForms([]string{oddKey})
	text := "a " + oddKey + " b " + url.QueryEscape(oddKey) + " c"
	for size := 1; size <= len(text); size++ {
		s := &scrubReader{src: &chunked{data: []byte(text), size: size}, closer: io.NopCloser(nil), forms: forms}
		raw, err := io.ReadAll(s)
		if err != nil {
			t.Fatal(err)
		}
		if want := "a " + redactMark + " b " + redactMark + " c"; string(raw) != want {
			t.Fatalf("chunks of %d: %q, want %q", size, raw, want)
		}
	}
}

type chunked struct {
	data []byte
	size int
}

func (c *chunked) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p[:min(len(p), c.size)], c.data)
	c.data = c.data[n:]
	return n, nil
}
