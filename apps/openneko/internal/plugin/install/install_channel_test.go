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

func TestConvertCapabilities_Connect(t *testing.T) {
	out := convertCapabilities(marketplace.Capabilities{
		Connect: &marketplace.ConnectCapability{
			ProviderLabel: "Google Workspace",
			Scopes:        []string{"gmail.send", "calendar"},
			Flow:          "oauth2-pkce",
		},
	})
	if out.Connect == nil {
		t.Fatal("connect capability dropped by convertCapabilities")
	}
	if out.Connect.ProviderLabel != "Google Workspace" {
		t.Fatalf("providerLabel: got %q", out.Connect.ProviderLabel)
	}
	if out.Connect.Flow != "oauth2-pkce" {
		t.Fatalf("flow: got %q", out.Connect.Flow)
	}
	if len(out.Connect.Scopes) != 2 || out.Connect.Scopes[0] != "gmail.send" {
		t.Fatalf("scopes: got %v", out.Connect.Scopes)
	}
}

func TestConvertOpennekoCapabilities_Connect(t *testing.T) {
	out := convertOpennekoCapabilities(&pkgCapabilities{
		Connect: &manifest.ConnectCapability{
			ProviderLabel: "Google Workspace",
			Scopes:        []string{"gmail.send"},
			Flow:          "oauth2-pkce",
		},
	})
	if out.Connect == nil || out.Connect.ProviderLabel != "Google Workspace" {
		t.Fatalf("connect capability not passed through: %+v", out.Connect)
	}
}

// Magic link declares manual provisioning. Losing it would let any mailbox sign in.
func TestConvertCapabilities_AuthProvisioning(t *testing.T) {
	out := convertCapabilities(marketplace.Capabilities{
		Auth: &marketplace.AuthCapability{ProviderLabel: "Email link", Provisioning: "manual", LoginHintRequired: true},
	})
	if out.Auth == nil || out.Auth.Provisioning != "manual" || !out.Auth.LoginHintRequired {
		t.Fatalf("auth declaration not round-tripped: %+v", out.Auth)
	}
}

func TestConvertCapabilities_Directory(t *testing.T) {
	write := json.RawMessage(`{"createUser":true,"deactivateUser":false}`)
	out := convertCapabilities(marketplace.Capabilities{
		Directory: &marketplace.DirectoryCapability{ProviderLabel: "Scalekit directory", Write: write},
	})
	if out.Directory == nil || out.Directory.ProviderLabel != "Scalekit directory" || string(out.Directory.Write) != string(write) {
		t.Fatalf("directory capability not round-tripped: %+v", out.Directory)
	}
}

func TestConvertOpennekoCapabilities_AuthAndDirectory(t *testing.T) {
	var pkg pkgCapabilities
	raw := `{"auth":{"providerLabel":"Email link","provisioning":"manual","loginHintRequired":true},"directory":{"providerLabel":"Scalekit directory","read":{"users":true}}}`
	if err := json.Unmarshal([]byte(raw), &pkg); err != nil {
		t.Fatal(err)
	}
	out := convertOpennekoCapabilities(&pkg)
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var round manifest.Capabilities
	if err := json.Unmarshal(encoded, &round); err != nil {
		t.Fatal(err)
	}
	if round.Auth == nil || round.Auth.Provisioning != "manual" || !round.Auth.LoginHintRequired {
		t.Fatalf("auth declaration lost: %s", encoded)
	}
	if round.Directory == nil || string(round.Directory.Read) != `{"users":true}` {
		t.Fatalf("directory declaration lost: %s", encoded)
	}
}
