package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestProcessCommandRejectsUntrustedBindingsBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		env         map[string]string
	}{
		{"oversize", strings.Repeat("x", maxRequestBytes+1), nil},
		{"extra-field", `{"Argv":["python3"],"Outputs":["a.csv"],"RunID":"forged"}`, nil},
		{"two-objects", `{"Argv":["python3"],"Outputs":["a.csv"]}{"Argv":["sh"],"Outputs":["b.csv"]}`, nil},
		{"missing-operation", `{"Argv":["python3"],"Outputs":["a.csv"]}`, nil},
		{"invalid-timeout", `{"Argv":["python3"],"Outputs":["a.csv"]}`, map[string]string{"HARNESS_PROCESS_OPERATION_ID": "1", "HARNESS_PROCESS_TIMEOUT_SECONDS": "forever"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			getenv := func(name string) string { return tc.env[name] }
			if err := run(context.Background(), getenv, strings.NewReader(tc.input), &out); err == nil {
				t.Fatal("unsafe process command accepted")
			}
			var receipt response
			if err := json.Unmarshal([]byte(out.String()), &receipt); err != nil || receipt.OK || receipt.Error == "" || receipt.Result != nil {
				t.Fatalf("invalid failure receipt: %q (%v)", out.String(), err)
			}
		})
	}
}
