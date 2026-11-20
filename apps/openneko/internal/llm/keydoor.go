package llm

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
)

// DoorRoute is one upstream that a grant may reach, with the real key.
type DoorRoute struct {
	Upstream string
	// Header names where the provider takes the key: "authorization",
	// "x-api-key", "x-goog-api-key" or "api-key".
	Header string
	Key    string
	Query  url.Values
	// API names the provider calls the route may carry: DoorChat when empty.
	API string
}

const (
	DoorChat      = "chat"
	DoorAnthropic = "anthropic"
	DoorGemini    = "gemini"
	DoorEmbed     = "embed"
)

// doorCall reports whether the route's API makes a call at tail with query.
func (r DoorRoute) doorCall(tail string, query url.Values) bool {
	for name, values := range query {
		_, replaced := r.Query[name]
		streamed := r.API == DoorGemini && name == "alt" && len(values) == 1 && values[0] == "sse"
		if !replaced && !streamed {
			return false
		}
	}
	switch r.API {
	case DoorAnthropic:
		return tail == "v1/messages"
	case DoorGemini:
		model, method, ok := strings.Cut(strings.TrimPrefix(tail, "models/"), ":")
		return ok && strings.HasPrefix(tail, "models/") && model != "" && !strings.Contains(model, "/") &&
			(method == "generateContent" || method == "streamGenerateContent")
	case DoorEmbed:
		return tail == "embeddings"
	}
	return tail == "chat/completions"
}

type doorGrant struct {
	org, purpose string
	secret       [32]byte
	routes       map[string]DoorRoute
	deadline     time.Time
	ctx          context.Context
	cancel       context.CancelFunc

	mu           sync.Mutex
	calls, limit int
}

func (g *doorGrant) take() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.calls >= g.limit {
		return false
	}
	g.calls++
	return true
}

// DoorPass is one issued token. Revoke ends it and cuts its open calls.
type DoorPass struct {
	Token  string
	door   *KeyDoor
	grant  *doorGrant
	revoke sync.Once
	id     string
}

func (p *DoorPass) Revoke() {
	p.revoke.Do(func() {
		p.door.mu.Lock()
		delete(p.door.grants, p.id)
		p.door.mu.Unlock()
		p.grant.cancel()
	})
}

// Calls is the number of calls the door forwarded for the token.
func (p *DoorPass) Calls() int {
	p.grant.mu.Lock()
	defer p.grant.mu.Unlock()
	return p.grant.calls
}

// Settle compares the door's count with the calls serve reported, and
// reports a difference to the door's Mismatch.
func (p *DoorPass) Settle(ctx context.Context, reported int) bool {
	counted := p.Calls()
	if counted == reported {
		return true
	}
	log.Printf("[key-door] org %q %s: the door forwarded %d model calls, serve reported %d", p.grant.org, p.grant.purpose, counted, reported)
	if p.door.Mismatch != nil {
		p.door.Mismatch(context.WithoutCancel(ctx), DoorMismatch{OrgID: p.grant.org, Purpose: p.grant.purpose, Counted: counted, Reported: reported})
	}
	return false
}

// DoorMismatch is a call whose count at the door differs from serve's report.
type DoorMismatch struct {
	OrgID, Purpose    string
	Counted, Reported int
}

// KeyDoor holds the model keys in the control zone and forwards the agent
// zone's model calls to their upstream with the key put in.
type KeyDoor struct {
	Transport http.RoundTripper
	Now       func() time.Time
	Mismatch  func(context.Context, DoorMismatch)

	mu     sync.Mutex
	grants map[string]*doorGrant
}

const (
	maxGrant      = 2 * time.Hour
	maxDoorBody   = 64 << 20
	maxDoorError  = 1 << 20
	doorIDBytes   = 16
	doorSecretLen = 32
)

func (d *KeyDoor) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Issue makes a token for one purpose. The token reaches only routes, for
// at most calls calls, and only until Revoke runs or ttl ends.
func (d *KeyDoor) Issue(org, purpose string, routes map[string]DoorRoute, ttl time.Duration, calls int) (*DoorPass, error) {
	if ttl <= 0 || ttl > maxGrant {
		ttl = maxGrant
	}
	if calls <= 0 {
		return nil, errors.New("the key door needs a call cap")
	}
	for name, r := range routes {
		u, err := url.Parse(r.Upstream)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" {
			return nil, fmt.Errorf("the key door cannot reach route %s: invalid upstream", name)
		}
	}
	raw := make([]byte, doorIDBytes+doorSecretLen)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	id := base64.RawURLEncoding.EncodeToString(raw[:doorIDBytes])
	token := keydoor.TokenPrefix + id + "." + base64.RawURLEncoding.EncodeToString(raw[doorIDBytes:])
	grant := &doorGrant{org: org, purpose: purpose, secret: sha256.Sum256(raw[doorIDBytes:]), routes: routes,
		deadline: d.now().Add(ttl), limit: calls}
	grant.ctx, grant.cancel = context.WithCancel(context.Background())
	d.mu.Lock()
	if d.grants == nil {
		d.grants = map[string]*doorGrant{}
	}
	d.grants[id] = grant
	d.mu.Unlock()
	return &DoorPass{Token: token, door: d, grant: grant, id: id}, nil
}

func (d *KeyDoor) grant(token string) *doorGrant {
	rest, ok := strings.CutPrefix(token, keydoor.TokenPrefix)
	if !ok {
		return nil
	}
	id, secret, ok := strings.Cut(rest, ".")
	if !ok {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(raw)
	d.mu.Lock()
	g := d.grants[id]
	if g != nil && !d.now().Before(g.deadline) {
		delete(d.grants, id)
		g = nil
	}
	d.mu.Unlock()
	if g == nil || subtle.ConstantTimeCompare(sum[:], g.secret[:]) != 1 {
		return nil
	}
	return g
}

var doorKeyHeaders = []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Api-Key"}

func tokenFrom(h http.Header) string {
	for _, name := range doorKeyHeaders {
		if v := strings.TrimSpace(h.Get(name)); v != "" {
			if name == "Authorization" {
				v = strings.TrimSpace(strings.TrimPrefix(v, "Bearer "))
			}
			return v
		}
	}
	return ""
}

func doorFail(w http.ResponseWriter, status int, text string) {
	http.Error(w, text, status)
}

func (d *KeyDoor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		doorFail(w, http.StatusMethodNotAllowed, "the key door forwards POST only")
		return
	}
	g := d.grant(tokenFrom(r.Header))
	if g == nil {
		doorFail(w, http.StatusUnauthorized, "the key door refused the token")
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/r/")
	if !ok || r.URL.RawPath != "" {
		doorFail(w, http.StatusNotFound, "the key door has no such route")
		return
	}
	name, tail, _ := strings.Cut(rest, "/")
	route, ok := g.routes[name]
	if !ok {
		doorFail(w, http.StatusForbidden, "the token does not hold this route")
		return
	}
	for _, part := range strings.Split(tail, "/") {
		if part == ".." || part == "." {
			doorFail(w, http.StatusNotFound, "the key door has no such route")
			return
		}
	}
	if !route.doorCall(tail, r.URL.Query()) {
		log.Printf("[key-door] route %s: refused a call that is not a model call", name)
		doorFail(w, http.StatusForbidden, "the key door forwards model calls only")
		return
	}
	if !g.take() {
		log.Printf("[key-door] route %s: refused a call over the cap of %d", name, g.limit)
		doorFail(w, http.StatusTooManyRequests, "the token used all its model calls")
		return
	}
	ctx, cut := context.WithCancel(r.Context())
	defer cut()
	stop := context.AfterFunc(g.ctx, cut)
	defer stop()
	r = r.WithContext(ctx)
	upstream, _ := url.Parse(route.Upstream)
	keys := []string{route.Key}
	proxy := &httputil.ReverseProxy{
		Transport:     d.transport(),
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			out.URL.Scheme, out.URL.Host = upstream.Scheme, upstream.Host
			out.URL.Path = strings.TrimRight(upstream.Path, "/")
			if tail != "" {
				out.URL.Path += "/" + tail
			}
			out.URL.RawPath = ""
			q := out.URL.Query()
			for k, v := range route.Query {
				q[k] = v
			}
			out.URL.RawQuery = q.Encode()
			out.Host = upstream.Host
			for _, h := range doorKeyHeaders {
				out.Header.Del(h)
			}
			out.Header.Del("Cookie")
			if route.Key != "" && route.Key != "none" {
				if route.Header == "authorization" || route.Header == "" {
					out.Header.Set("Authorization", "Bearer "+route.Key)
				} else {
					out.Header.Set(route.Header, route.Key)
				}
			}
		},
		ModifyResponse: func(res *http.Response) error {
			if res.StatusCode >= 200 && res.StatusCode < 300 {
				return nil
			}
			body, err := io.ReadAll(io.LimitReader(res.Body, maxDoorError))
			_ = res.Body.Close()
			if err != nil {
				body = nil
			}
			body = []byte(redactKeys(string(body), keys))
			res.Body = io.NopCloser(bytes.NewReader(body))
			res.ContentLength = int64(len(body))
			res.Header.Del("Content-Encoding")
			res.Header.Set("Content-Length", fmt.Sprint(len(body)))
			log.Printf("[key-door] route %s answered HTTP %d", name, res.StatusCode)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			log.Printf("[key-door] route %s unavailable: %s", name, redactKeys(err.Error(), keys))
			doorFail(w, http.StatusBadGateway, "the model provider did not answer")
		},
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDoorBody)
	proxy.ServeHTTP(w, r)
}

func (d *KeyDoor) transport() http.RoundTripper {
	if d.Transport != nil {
		return d.Transport
	}
	return doorTransport
}

var doorTransport = func() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = http.ProxyFromEnvironment
	return t
}()

func redactKeys(text string, keys []string) string {
	for _, k := range keys {
		if len(k) >= 4 && k != "none" {
			text = strings.ReplaceAll(text, k, "[redacted]")
		}
	}
	return text
}

var doorAPIs = map[string]string{
	"anthropic":     DoorAnthropic,
	"google-gemini": DoorGemini,
}

var doorHeaders = map[string]string{
	"anthropic":     "x-api-key",
	"google-gemini": "x-goog-api-key",
	"azure-openai":  "api-key",
}

// DoorRouteFor is the upstream of an Ax route: the URL Ax would call when
// the route carries no door URL.
func DoorRouteFor(route AxModelRoute, key string) (DoorRoute, error) {
	r := DoorRoute{Header: doorHeaders[route.Provider], Key: key, API: doorAPIs[route.Provider]}
	if r.Header == "" {
		r.Header = "authorization"
	}
	switch {
	case route.Provider == "azure-openai":
		resource, deployment := route.Options["resource_name"], route.Options["deployment_name"]
		if !dnsLabel.MatchString(resource) || deployment == "" {
			return DoorRoute{}, errors.New("azure-openai needs resource_name and deployment_name")
		}
		r.Upstream = "https://" + resource + ".openai.azure.com/openai/deployments/" + url.PathEscape(deployment)
		version := route.Options["api_version"]
		if version == "" {
			version = AxAzureAPIVersion
		}
		r.Query = url.Values{"api-version": {version}}
	case route.URL != "":
		r.Upstream = route.URL
	default:
		fixed, ok := fixedProviders[route.Provider]
		if !ok {
			return DoorRoute{}, fmt.Errorf("model provider %q needs a URL", route.Provider)
		}
		r.Upstream = fixed[0]
	}
	return r, nil
}
