package packs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type preflightParityFixture struct {
	ReadinessCases []struct {
		Name          string              `json:"name"`
		Kind          string              `json:"kind"`
		Definition    PackActionReadiness `json:"definition"`
		ExpectedError *string             `json:"expectedError"`
	} `json:"readinessCases"`
}

func TestPackActionPreflightParity(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "pack_action_preflight_parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx preflightParityFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fx.ReadinessCases {
		err := AssertPackActionReady(tc.Kind, tc.Definition)
		if tc.ExpectedError == nil {
			if err != nil {
				t.Errorf("%s: unexpected error %v", tc.Name, err)
			}
			continue
		}
		if err == nil || err.Error() != *tc.ExpectedError {
			t.Errorf("%s: got %v, want %q", tc.Name, err, *tc.ExpectedError)
		}
	}
}

func TestAssertSafePackActionPayload(t *testing.T) {
	if err := AssertSafePackActionPayloadJSON(map[string]any{"operation": "send", "body": map[string]any{"to": []any{"ops@acme.com"}}}); err != nil {
		t.Fatalf("safe payload: %v", err)
	}
	for _, unsafeKey := range []string{"__proto__", "prototype", "constructor"} {
		err := AssertSafePackActionPayloadJSON(map[string]any{"nested": []any{map[string]any{unsafeKey: "bad"}}})
		if err == nil || err.Error() != "pack action payload contains an unsafe key" {
			t.Errorf("%s: got %v", unsafeKey, err)
		}
	}
	huge := map[string]any{"blob": strings.Repeat("a", 1_000_001)}
	if err := AssertSafePackActionPayloadJSON(huge); err == nil || err.Error() != "pack action payload is too large" {
		t.Errorf("huge payload: got %v", err)
	}
}
