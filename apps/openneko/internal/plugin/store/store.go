package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/plugin/marketplace"
)

const Filename = "marketplaces.json"

type TrustedMarketplace struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	AddedAt  string `json:"addedAt"`
	Official bool   `json:"official,omitempty"`
}

type Store struct {
	Marketplaces []TrustedMarketplace `json:"marketplaces"`
}

func Path(overrideDir string) string {
	return filepath.Join(config.Dir(overrideDir), Filename)
}

func defaultStore() Store {
	return Store{Marketplaces: []TrustedMarketplace{
		{
			Name:     marketplace.OfficialName,
			URL:      marketplace.OfficialURL,
			AddedAt:  "1970-01-01",
			Official: true,
		},
	}}
}

func Read(overrideDir string) (Store, error) {
	file := Path(overrideDir)
	raw, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s := defaultStore()
			if err := Write(s, overrideDir); err != nil {
				return Store{}, err
			}
			return s, nil
		}
		return Store{}, err
	}
	var s Store
	if err := json.Unmarshal(raw, &s); err != nil {
		return Store{}, fmt.Errorf("marketplaces config at %s is invalid JSON: %w", file, err)
	}
	if s.Marketplaces == nil {
		return Store{}, fmt.Errorf("marketplaces config at %s has unexpected shape", file)
	}
	hasOfficial := false
	for _, m := range s.Marketplaces {
		if m.Name == marketplace.OfficialName {
			hasOfficial = true
			break
		}
	}
	if !hasOfficial {
		official := defaultStore().Marketplaces[0]
		s.Marketplaces = append([]TrustedMarketplace{official}, s.Marketplaces...)
		if err := Write(s, overrideDir); err != nil {
			return Store{}, err
		}
	}
	return s, nil
}

func Write(s Store, overrideDir string) error {
	file := Path(overrideDir)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(file, b, 0o644)
}

var slugRX = regexp.MustCompile(`[^a-z0-9]+`)
var slugTrimRX = regexp.MustCompile(`^-+|-+$`)

func Slugify(name string) string {
	out := slugRX.ReplaceAllString(strings.ToLower(name), "-")
	out = slugTrimRX.ReplaceAllString(out, "")
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

func Add(s Store, entry TrustedMarketplace) (Store, error) {
	for _, m := range s.Marketplaces {
		if m.Name == entry.Name {
			return s, fmt.Errorf(`marketplace %q already trusted — remove it first if you want to change its URL`, entry.Name)
		}
		if m.URL == entry.URL {
			return s, fmt.Errorf("marketplace URL %s already trusted", entry.URL)
		}
	}
	return Store{Marketplaces: append(append([]TrustedMarketplace(nil), s.Marketplaces...), entry)}, nil
}

func Remove(s Store, nameOrURL string) (Store, *TrustedMarketplace, error) {
	idx := -1
	for i, m := range s.Marketplaces {
		if m.Name == nameOrURL || m.URL == nameOrURL {
			idx = i
			break
		}
	}
	if idx == -1 {
		return s, nil, nil
	}
	target := s.Marketplaces[idx]
	if target.Official {
		return s, nil, fmt.Errorf(`marketplace %q is the official OpenNeko marketplace — refusing to remove`, target.Name)
	}
	out := make([]TrustedMarketplace, 0, len(s.Marketplaces)-1)
	out = append(out, s.Marketplaces[:idx]...)
	out = append(out, s.Marketplaces[idx+1:]...)
	return Store{Marketplaces: out}, &target, nil
}
