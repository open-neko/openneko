package confine

import (
	"os"
	"strings"
)

func machineID() string {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if raw, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(raw)) != "" {
			return "machine-id:" + strings.TrimSpace(string(raw))
		}
	}
	return ""
}
