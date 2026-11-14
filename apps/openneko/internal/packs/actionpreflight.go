package packs

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
)

// PackActionReadiness mirrors PackActionReadiness in apps/worker/src/packs/action-preflight.ts.
type PackActionReadiness struct {
	Enabled   bool    `json:"enabled"`
	Readiness string  `json:"readiness"`
	Reason    *string `json:"reason"`
}

// AssertPackActionReady matches assertPackActionReady() in apps/worker/src/packs/action-preflight.ts.
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

// AssertSafePackActionPayload matches assertSafePayload() in apps/worker/src/packs/declarative-action-runtime.ts.
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

// LockPackActionBody matches lockBody() in apps/worker/src/packs/declarative-action-runtime.ts.
// When hasBody is false, body was undefined in the action payload.
func LockPackActionBody(
	kind string,
	body any,
	hasBody bool,
	locked map[string]string,
	lockedOrder []string,
	config map[string]any,
) (any, bool, error) {
	if len(locked) == 0 {
		return body, hasBody, nil
	}
	out := map[string]any{}
	if hasBody {
		m, ok := body.(map[string]any)
		if !ok || m == nil {
			return nil, false, errors.New("pack action body must be an object")
		}
		maps.Copy(out, m)
	}
	keys := lockedOrder
	if len(keys) == 0 {
		keys = slices.Sorted(maps.Keys(locked))
	}
	for _, field := range keys {
		input, ok := locked[field]
		if !ok {
			continue
		}
		val, exists := config[input]
		if !exists || val == nil || val == "" {
			return nil, false, fmt.Errorf("pack action %s needs the pack input %s for body field %s", kind, input, field)
		}
		out[field] = val
	}
	return out, true, nil
}
