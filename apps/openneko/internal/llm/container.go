package llm

import (
	"os"
	"strconv"
	"strings"
)

var dockerEnvFile = "/.dockerenv"

// InContainer is true when OPENNEKO_IN_CONTAINER is true, or when it is unset
// and /.dockerenv exists.
func InContainer() bool {
	if v := strings.TrimSpace(os.Getenv("OPENNEKO_IN_CONTAINER")); v != "" {
		on, err := strconv.ParseBool(v)
		return err == nil && on
	}
	_, err := os.Stat(dockerEnvFile)
	return err == nil
}
