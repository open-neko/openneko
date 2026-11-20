// Package keydoor is the agent zone's side of the key door: the process in
// the control zone that holds the model keys and forwards model calls.
//
// The control zone gives serve a token in place of each key and a door URL
// in place of each provider URL. A request to the door URL goes over the
// door's Unix socket; the URL's host only marks it.
package keydoor

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
)

const (
	Host        = "openneko-door"
	TokenPrefix = "ondoor_"
	SocketName  = "keydoor.sock"
	SocketEnv   = "OPENNEKO_KEY_DOOR_SOCKET"
)

// SocketPath is the door's socket: the variable when set, else a file
// beside the serve socket.
func SocketPath(getenv func(string) string, serveSocket string) string {
	if path := strings.TrimSpace(getenv(SocketEnv)); path != "" {
		return path
	}
	if serveSocket == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(serveSocket), SocketName)
}

// URL is the base URL that stands in for the upstream of one route.
func URL(route string) string { return "http://" + Host + "/r/" + route }

func IsURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host == Host
}

func IsToken(value string) bool {
	return strings.HasPrefix(value, TokenPrefix) && len(value) > len(TokenPrefix)+32
}

var (
	mu     sync.RWMutex
	socket string
	client *http.Client
)

// SetSocket names the door's socket for this process. Only the process sets
// it; a frame cannot.
func SetSocket(path string) {
	mu.Lock()
	defer mu.Unlock()
	socket, client = path, nil
	if path == "" {
		return
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
		MaxIdleConnsPerHost: 16,
		DisableCompression:  true,
	}
	client = &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("the key door redirected") },
	}
}

var ErrNoDoor = errors.New("the key door is not set up: OPENNEKO_KEY_DOOR_SOCKET or the serve socket is missing")

// Client reaches the door. Every request goes to the socket, whatever its
// URL names.
func Client() (*http.Client, error) {
	mu.RLock()
	defer mu.RUnlock()
	if client == nil {
		return nil, ErrNoDoor
	}
	return client, nil
}
