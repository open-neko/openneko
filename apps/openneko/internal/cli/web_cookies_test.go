package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/webfront"
)

func TestWebAPICookiesFollowThePublicURLScheme(t *testing.T) {
	for _, c := range []struct {
		publicURL, env string
		secure         bool
		warns          bool
	}{
		{"https://openneko.example.com", "", true, false},
		{"https://openneko.example.com", "development", true, false},
		{"http://192.168.1.20:3000", "", false, true},
		{"http://openneko.lan:3000", "production", false, true},
		{"HTTP://openneko.lan:3000", "", false, true},
		{"http://localhost:3000", "", false, false},
		{"http://127.0.0.1:3000", "", false, false},
		{"", "", true, false},
		{"", "development", false, false},
	} {
		t.Setenv("OPENNEKO_PUBLIC_URL", c.publicURL)
		t.Setenv("OPENNEKO_ENV", c.env)
		t.Setenv("NODE_ENV", "")
		var errOut bytes.Buffer
		api := newWebAPI(&webfront.Gate{}, nil, "test", t.TempDir(), func() {}, nil, nil, &errOut)
		if api.SecureCookies != c.secure {
			t.Errorf("public URL %q, OPENNEKO_ENV %q: Secure %v, want %v", c.publicURL, c.env, api.SecureCookies, c.secure)
		}
		if warned := strings.Contains(errOut.String(), "not Secure"); warned != c.warns {
			t.Errorf("public URL %q: warning %q, want a warning %v", c.publicURL, errOut.String(), c.warns)
		}
		if strings.Count(errOut.String(), "\n") > 1 {
			t.Errorf("public URL %q: more than one warning line: %q", c.publicURL, errOut.String())
		}
	}
}
