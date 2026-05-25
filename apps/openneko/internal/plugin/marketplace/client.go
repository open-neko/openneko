package marketplace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	OfficialName = "official"
	OfficialURL  = "https://open-neko.github.io/plugins/marketplace.json"
)

type EnvRequirement struct {
	Key         string `json:"key"`
	Required    *bool  `json:"required,omitempty"`
	Secret      *bool  `json:"secret,omitempty"`
	Description string `json:"description"`
}

type Permissions struct {
	Network []string         `json:"network"`
	Env     []EnvRequirement `json:"env"`
}

type ActionDeclaration struct {
	Kind        string          `json:"kind"`
	Description string          `json:"description"`
	DefaultMode json.RawMessage `json:"default_mode,omitempty"`
}

type ActionCapability struct {
	Kinds []ActionDeclaration `json:"kinds"`
}

type AuthCapability struct {
	ProviderLabel string `json:"providerLabel,omitempty"`
}

// ChannelCapability — a frontend (Slack, Telegram, voice, …). Profile is
// carried as raw JSON; the worker validates it.
type ChannelCapability struct {
	ProviderLabel string          `json:"providerLabel"`
	Profile       json.RawMessage `json:"profile,omitempty"`
	Directions    []string        `json:"directions,omitempty"`
	Ingress       string          `json:"ingress,omitempty"`
}

type Capabilities struct {
	Action  *ActionCapability  `json:"action,omitempty"`
	Auth    *AuthCapability    `json:"auth,omitempty"`
	Channel *ChannelCapability `json:"channel,omitempty"`
}

type Version struct {
	Version      string       `json:"version"`
	Integrity    string       `json:"integrity"`
	Permissions  Permissions  `json:"permissions"`
	Capabilities Capabilities `json:"capabilities"`
	PublishedAt  string       `json:"publishedAt"`
	Yanked       bool         `json:"yanked,omitempty"`
	YankedReason string       `json:"yanked_reason,omitempty"`
}

type Plugin struct {
	Name        string    `json:"name"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Source      string    `json:"source"`
	Homepage    string    `json:"homepage,omitempty"`
	Versions    []Version `json:"versions"`
}

type Marketplace struct {
	Name        string   `json:"name"`
	Owner       string   `json:"owner"`
	Description string   `json:"description"`
	Homepage    string   `json:"homepage,omitempty"`
	Plugins     []Plugin `json:"plugins"`
}

// Client fetches marketplace JSON documents.
type Client interface {
	Fetch(ctx context.Context, url string) (*Marketplace, error)
}

type httpClient struct {
	hc *http.Client
}

func NewClient() Client {
	return &httpClient{hc: &http.Client{Timeout: 30 * time.Second}}
}

func (c *httpClient) Fetch(ctx context.Context, url string) (*Marketplace, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("marketplace: %s returned %d %s", url, res.StatusCode, res.Status)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	var m Marketplace
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("marketplace: %s: %w", url, err)
	}
	if m.Name == "" || m.Owner == "" || m.Description == "" || m.Plugins == nil {
		return nil, fmt.Errorf("marketplace: %s did not match the expected shape", url)
	}
	return &m, nil
}

func FindPlugin(m *Marketplace, name string) *Plugin {
	if m == nil {
		return nil
	}
	for i := range m.Plugins {
		if m.Plugins[i].Name == name {
			return &m.Plugins[i]
		}
	}
	return nil
}

func PickInstallVersion(p *Plugin, requested string) (*Version, error) {
	if p == nil {
		return nil, fmt.Errorf("nil plugin")
	}
	live := make([]Version, 0, len(p.Versions))
	for _, v := range p.Versions {
		if !v.Yanked {
			live = append(live, v)
		}
	}
	if len(live) == 0 {
		return nil, fmt.Errorf("marketplace: every published version of %s is yanked", p.Name)
	}
	if requested != "" {
		for i := range live {
			if live[i].Version == requested {
				return &live[i], nil
			}
		}
		return nil, fmt.Errorf("marketplace: %s has no published non-yanked version %s", p.Name, requested)
	}
	latest := &live[0]
	for i := 1; i < len(live); i++ {
		if Compare(live[i].Version, latest.Version) > 0 {
			latest = &live[i]
		}
	}
	return latest, nil
}
