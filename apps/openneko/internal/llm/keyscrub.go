package llm

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const redactMark = "[redacted]"

var percentHex = regexp.MustCompile(`%[0-9A-F]{2}`)

// keyForms are the byte strings that show a key: raw, JSON-escaped,
// URL-encoded and base64 at each of the three byte alignments.
func keyForms(keys []string) [][]byte {
	seen := map[string]bool{}
	var forms [][]byte
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			forms = append(forms, []byte(s))
		}
	}
	for _, k := range keys {
		if len(k) < 4 || k == "none" {
			continue
		}
		add(k)
		quoted, _ := json.Marshal(k)
		add(string(quoted[1 : len(quoted)-1]))
		var plain bytes.Buffer
		enc := json.NewEncoder(&plain)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(k)
		add(strings.TrimSuffix(strings.TrimSuffix(plain.String(), "\n"), `"`)[1:])
		for _, escaped := range []string{url.QueryEscape(k), url.PathEscape(k)} {
			add(escaped)
			add(percentHex.ReplaceAllStringFunc(escaped, strings.ToLower))
		}
		for _, b64 := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
			for pad := 0; pad < 3; pad++ {
				s := b64.EncodeToString(append(make([]byte, pad), k...))
				add(s[(pad*8+5)/6 : (pad+len(k))*8/6])
			}
		}
	}
	return forms
}

// scrubStep replaces each whole form in buf and returns the part that is
// safe to send. rest is a tail that may start a form; final sends it too.
func scrubStep(buf []byte, forms [][]byte, final bool) (out, rest []byte) {
	for {
		at, n := -1, 0
		for _, f := range forms {
			if i := bytes.Index(buf, f); i >= 0 && (at < 0 || i < at || (i == at && len(f) > n)) {
				at, n = i, len(f)
			}
		}
		if at < 0 {
			break
		}
		out = append(out, buf[:at]...)
		out = append(out, redactMark...)
		buf = buf[at+n:]
	}
	hold := 0
	if !final {
		for _, f := range forms {
			for k := min(len(buf), len(f)-1); k > hold; k-- {
				if bytes.HasPrefix(f, buf[len(buf)-k:]) {
					hold = k
					break
				}
			}
		}
	}
	out = append(out, buf[:len(buf)-hold]...)
	return out, append([]byte(nil), buf[len(buf)-hold:]...)
}

func scrubText(text string, forms [][]byte) string {
	out, _ := scrubStep([]byte(text), forms, true)
	return string(out)
}

func scrubHeader(h http.Header, forms [][]byte) {
	for name, values := range h {
		for i, v := range values {
			values[i] = scrubText(v, forms)
		}
		h[name] = values
	}
}

type scrubReader struct {
	src      io.Reader
	closer   io.Closer
	forms    [][]byte
	held     []byte
	out      []byte
	err      error
	buf      [32 << 10]byte
	atEOF    func()
	finished bool
}

func (s *scrubReader) Read(p []byte) (int, error) {
	for len(s.out) == 0 {
		if s.finished {
			return 0, s.err
		}
		n, err := s.src.Read(s.buf[:])
		if err != nil {
			s.out, s.held = scrubStep(append(s.held, s.buf[:n]...), s.forms, true)
			s.finished, s.err = true, err
			if s.atEOF != nil {
				s.atEOF()
			}
			continue
		}
		s.out, s.held = scrubStep(append(s.held, s.buf[:n]...), s.forms, false)
	}
	n := copy(p, s.out)
	s.out = s.out[n:]
	return n, nil
}

func (s *scrubReader) Close() error { return s.closer.Close() }

// scrubResponse decodes the body and removes every form of the keys from
// the body, the headers and the trailers. An encoding the door cannot
// read is refused, because the door cannot scrub it.
func scrubResponse(res *http.Response, forms [][]byte, limit int64) error {
	var body io.Reader = res.Body
	switch enc := strings.ToLower(strings.TrimSpace(res.Header.Get("Content-Encoding"))); enc {
	case "", "identity":
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(res.Body)
		if err != nil {
			return fmt.Errorf("unreadable gzip answer: %w", err)
		}
		body = zr
	case "deflate":
		zr, err := zlib.NewReader(res.Body)
		if err != nil {
			return fmt.Errorf("unreadable deflate answer: %w", err)
		}
		body = zr
	default:
		return fmt.Errorf("the answer has content encoding %q", enc)
	}
	if limit > 0 {
		body = io.LimitReader(body, limit)
	}
	res.Header.Del("Content-Encoding")
	res.Header.Del("Content-Length")
	res.ContentLength = -1
	scrubHeader(res.Header, forms)
	trailer := res.Trailer
	res.Body = &scrubReader{src: body, closer: res.Body, forms: forms, atEOF: func() { scrubHeader(trailer, forms) }}
	return nil
}
