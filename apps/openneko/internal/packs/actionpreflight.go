package packs

import (
	"encoding/json"
	"errors"
	"fmt"
)

type PackActionReadiness struct {
	Enabled   bool    `json:"enabled"`
	Readiness string  `json:"readiness"`
	Reason    *string `json:"reason"`
}

func AssertPackActionReady(kind string, definition PackActionReadiness) error {
	if definition.Enabled && definition.Readiness == "ready" {
		return nil
	}
	reason := "the pack operator is unavailable"
	if definition.Reason != nil {
		if trimmed := jsTrim(*definition.Reason); trimmed != "" {
			reason = trimmed
		}
	}
	return fmt.Errorf("pack action %s is blocked: %s", kind, reason)
}

func AssertSafePackActionPayload(serializedLen int, value any) error {
	if serializedLen > 1_000_000 {
		return errors.New("pack action payload is too large")
	}
	var inspect func(current any) error
	inspect = func(current any) error {
		switch v := current.(type) {
		case []any:
			for _, item := range v {
				if err := inspect(item); err != nil {
					return err
				}
			}
		case map[string]any:
			for k, child := range v {
				if k == "__proto__" || k == "prototype" || k == "constructor" {
					return errors.New("pack action payload contains an unsafe key")
				}
				if err := inspect(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return inspect(value)
}

// AssertSafePackActionPayloadJSON checks a JSON-marshallable value against AssertSafePackActionPayload.
func AssertSafePackActionPayloadJSON(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return AssertSafePackActionPayload(len(raw), value)
}
