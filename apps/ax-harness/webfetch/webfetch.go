// Package webfetch is the web_fetch tool. It runs in openneko web, which
// holds the host allowlist; the agent runner reaches it over the bridge.
package webfetch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// maxWebChars matches the Hermes web_extract stored-text ceiling. The agent
// result layer keeps up to 100,000 characters inline and saves the rest.
const (
	maxWebBody      = 10 << 20
	maxWebChars     = 2_000_000
	maxWebRedirects = 5
	webTimeout      = 30 * time.Second
)

// BridgeTool is the bridge tool that the runner's web_fetch calls.
const BridgeTool = "openneko_web_fetch"

// InputSchema is web_fetch's input.
const InputSchema = `{"type":"object","required":["url"],"properties":{"url":{"type":"string","minLength":1,"maxLength":4096}},"additionalProperties":false}`

// Web is a GET-only fetch tool limited to an admin host allowlist.
type Web struct {
	hosts   []string
	client  *http.Client
	allowIP func(net.IP) bool
	proxy   func(*http.Request) (*url.URL, error)
	lookup  func(ctx context.Context, host string) ([]net.IPAddr, error)
}

// ParseHosts reads a comma list of hosts. "*.example.com" matches
// subdomains and "*" matches any public host.
func ParseHosts(allowlist string) ([]string, error) {
	var hosts []string
	for _, host := range strings.Split(allowlist, ",") {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" {
			continue
		}
		if strings.ContainsAny(host, "/:@ ") || strings.Count(host, "*") > 1 || strings.Contains(host, "*") && host != "*" && !strings.HasPrefix(host, "*.") {
			return nil, fmt.Errorf("invalid web host %q", host)
		}
		hosts = append(hosts, host)
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("the web host allowlist is empty")
	}
	return hosts, nil
}

// Description is web_fetch's description for the model.
func Description(hosts []string) string {
	return "GET a web page or API response from an allowed host. HTML returns as plain text; JSON, XML and text return as received. Returns {url,status,content_type,content,truncated}. Allowed hosts: " + strings.Join(hosts, ", ") + "."
}

var errNotPublic = errors.New("web_fetch reaches public addresses only")

// Open takes a comma list of hosts, as ParseHosts reads it.
func Open(allowlist string) (*Web, error) {
	hosts, err := ParseHosts(allowlist)
	if err != nil {
		return nil, err
	}
	w := &Web{allowIP: publicIP, hosts: hosts, proxy: http.ProxyFromEnvironment, lookup: net.DefaultResolver.LookupIPAddr}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	// The operator's proxy may sit on a private address. Every other
	// connection must reach a public address.
	transport := &http.Transport{Proxy: w.proxyFor, ForceAttemptHTTP2: true, TLSHandshakeTimeout: 10 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address == w.proxyAddress() {
				return dialer.DialContext(ctx, network, address)
			}
			public := *dialer
			public.Control = func(_, address string, _ syscall.RawConn) error {
				host, _, err := net.SplitHostPort(address)
				if ip := net.ParseIP(host); err != nil || ip == nil || !w.allowIP(ip) {
					return errNotPublic
				}
				return nil
			}
			return public.DialContext(ctx, network, address)
		}}
	w.client = &http.Client{Transport: transport, Timeout: webTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxWebRedirects {
				return fmt.Errorf("too many redirects")
			}
			return w.check(req.URL)
		}}
	return w, nil
}

func (w *Web) proxyAddress() string {
	u, _ := w.proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "example.com"}})
	if u == nil {
		return ""
	}
	if u.Port() == "" {
		return net.JoinHostPort(u.Hostname(), map[string]string{"http": "80", "https": "443"}[u.Scheme])
	}
	return u.Host
}

// proxyFor sends a request through the proxy only when every address of its
// host is public. The proxy resolves the name again, so a name that changes
// its answer between the two lookups is not covered.
func (w *Web) proxyFor(req *http.Request) (*url.URL, error) {
	u, err := w.proxy(req)
	if err != nil || u == nil {
		return u, err
	}
	host := req.URL.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if !w.allowIP(ip) {
			return nil, errNotPublic
		}
		return u, nil
	}
	addrs, err := w.lookup(req.Context(), host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no address for %s", host)
	}
	for _, a := range addrs {
		if !w.allowIP(a.IP) {
			return nil, errNotPublic
		}
	}
	return u, nil
}

var notPublic = []*net.IPNet{
	{IP: net.IP{100, 64, 0, 0}, Mask: net.CIDRMask(10, 32)},
	{IP: net.ParseIP("64:ff9b::"), Mask: net.CIDRMask(96, 128)},
	{IP: net.ParseIP("64:ff9b:1::"), Mask: net.CIDRMask(48, 128)},
}

func publicIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, n := range notPublic {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

func (w *Web) check(u *url.URL) error {
	if u.Scheme != "https" && u.Scheme != "http" || u.User != nil || u.Hostname() == "" {
		return fmt.Errorf("web_fetch takes an http or https URL")
	}
	host := strings.ToLower(u.Hostname())
	for _, rule := range w.hosts {
		if rule == "*" || rule == host || strings.HasPrefix(rule, "*.") && strings.HasSuffix(host, rule[1:]) {
			return nil
		}
	}
	return fmt.Errorf("host %s is not on the web allowlist", host)
}

// Fetch runs one web_fetch call.
func (w *Web) Fetch(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var input struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("invalid web fetch")
	}
	target, err := url.Parse(input.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL")
	}
	if err := w.check(target); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "OpenNeko-Ax/1")
	req.Header.Set("Accept", "text/html, application/json, text/*;q=0.9, */*;q=0.1")
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWebBody+1))
	if err != nil {
		return nil, err
	}
	truncated := len(body) > maxWebBody
	if truncated {
		body = body[:maxWebBody]
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	var content string
	switch {
	case mediaType == "text/html" || mediaType == "application/xhtml+xml":
		content = htmlText(body)
	case strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || mediaType == "application/xml" ||
		strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml") || mediaType == "" && utf8.Valid(body):
		content = strings.ToValidUTF8(string(body), "�")
	default:
		return nil, fmt.Errorf("web_fetch reads text only, not %s", mediaType)
	}
	if utf8.RuneCountInString(content) > maxWebChars {
		content, truncated = runePrefix(content, maxWebChars), true
	}
	return json.Marshal(struct {
		URL         string `json:"url"`
		Status      int    `json:"status"`
		ContentType string `json:"content_type"`
		Content     string `json:"content"`
		Truncated   bool   `json:"truncated"`
	}{resp.Request.URL.String(), resp.StatusCode, mediaType, content, truncated})
}

var skipTags = map[string]bool{"script": true, "style": true, "noscript": true, "svg": true, "template": true, "head": true}
var blockTags = map[string]bool{"p": true, "div": true, "br": true, "li": true, "tr": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "section": true, "article": true, "pre": true, "table": true, "ul": true, "ol": true}

// htmlText keeps the visible text of a page, one block per line.
func htmlText(body []byte) string {
	var out strings.Builder
	tokens := html.NewTokenizer(bytes.NewReader(body))
	skip := 0
	for {
		kind := tokens.Next()
		switch kind {
		case html.ErrorToken:
			lines := strings.Split(out.String(), "\n")
			kept := lines[:0]
			for _, line := range lines {
				if line = strings.Join(strings.Fields(line), " "); line != "" {
					kept = append(kept, line)
				}
			}
			return strings.Join(kept, "\n")
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := tokens.TagName()
			if skipTags[string(name)] && kind == html.StartTagToken {
				skip++
			} else if blockTags[string(name)] {
				out.WriteByte('\n')
			}
		case html.EndTagToken:
			name, _ := tokens.TagName()
			if skipTags[string(name)] && skip > 0 {
				skip--
			} else if blockTags[string(name)] {
				out.WriteByte('\n')
			}
		case html.TextToken:
			if skip == 0 {
				out.Write(tokens.Text())
				out.WriteByte(' ')
			}
		}
	}
}

func runePrefix(text string, limit int) string {
	runes := []rune(text)
	return string(runes[:limit])
}
