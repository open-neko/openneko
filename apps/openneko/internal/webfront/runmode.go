package webfront

import (
	"os"
	"strings"
)

// Development reports whether openneko web runs in development mode. Production
// is the default. NODE_ENV is read only when OPENNEKO_ENV is unset, so a compose
// file from 5.0 still works for one release.
func Development() bool {
	if mode := strings.TrimSpace(os.Getenv("OPENNEKO_ENV")); mode != "" {
		return mode == "development"
	}
	return strings.TrimSpace(os.Getenv("NODE_ENV")) == "development"
}
