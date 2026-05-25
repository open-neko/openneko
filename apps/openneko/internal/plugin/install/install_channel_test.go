package install

import (
	"encoding/json"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/plugin/manifest"
	"github.com/open-neko/neko/apps/openneko/internal/plugin/marketplace"
)

// A marketplace version that declares only a channel capability must survive
// conversion into the installed manifest — including its opaque profile.
func TestConvertCapabilities_Channel(t *testing.T) {
	profile := json.RawMessage(`{"modalities":["text"],"fidelity":"summary"}`)
	out := convertCapabilities(marketplace.Capabilities{
		Channel: &marketplace.ChannelCapability{
			ProviderLabel: "Telegram",
			Profile:       profile,
			Directions:    []string{"outbound", "inbound"},
			Ingress:       "webhook",
		},
	})
	if out.Channel == nil {
		t.Fatal("channel capability dropped by convertCapabilities")
	}
	if out.Channel.ProviderLabel != "Telegram" {
		t.Fatalf("providerLabel: got %q", out.Channel.ProviderLabel)
	}
	if out.Channel.Ingress != "webhook" {
		t.Fatalf("ingress: got %q", out.Channel.Ingress)
	}
	if string(out.Channel.Profile) != string(profile) {
		t.Fatalf("profile not round-tripped: got %s", out.Channel.Profile)
	}
	if len(out.Channel.Directions) != 2 {
		t.Fatalf("directions: got %v", out.Channel.Directions)
	}
}

// The --unverified path reads capabilities from the package's own package.json;
// a channel must pass through there too.
func TestConvertOpennekoCapabilities_Channel(t *testing.T) {
	out := convertOpennekoCapabilities(&pkgCapabilities{
		Channel: &manifest.ChannelCapability{
			ProviderLabel: "Telegram",
			Directions:    []string{"outbound"},
		},
	})
	if out.Channel == nil || out.Channel.ProviderLabel != "Telegram" {
		t.Fatalf("channel capability not passed through: %+v", out.Channel)
	}
}
