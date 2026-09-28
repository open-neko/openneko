package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRecoveryIndexBoundsLargeSavedData(t *testing.T) {
	result := json.RawMessage(`{"payload":"` + strings.Repeat("X", 200_000) + `"}`)
	operations := []SavedOperation{{ID: 1, Instruction: "read", Result: result, Finished: true}}
	index := recoveryProjection(operations)
	if len(index) > 4096 || !strings.Contains(index, "result_ref") || strings.Contains(index, strings.Repeat("X", 1000)) {
		t.Fatalf("unbounded recovery index: %d bytes", len(index))
	}
	if _, err := savedOperationValue(operations, float64(2)); err == nil {
		t.Fatal("out-of-run saved operation was readable")
	}
	if _, err := savedOperationValue(operations, "1"); err == nil {
		t.Fatal("untyped saved operation ID accepted")
	}
	value, err := savedOperationValue(operations, float64(1))
	if err != nil {
		t.Fatal(err)
	}
	decoded := value.(map[string]any)
	if decoded["result"].(map[string]any)["payload"] != strings.Repeat("X", 200_000) {
		t.Fatal("retrieved evidence changed")
	}
}

func TestRecoveryIndexHasGlobalBound(t *testing.T) {
	operations := make([]SavedOperation, 32)
	for i := range operations {
		operations[i] = SavedOperation{ID: i + 1, Instruction: "read:" + strings.Repeat("\x00", 900),
			Result: json.RawMessage(`{"payload":"` + strings.Repeat("X", 4000) + `"}`), Finished: true}
	}
	index := recoveryProjection(operations)
	if len(index) > maxRecoveryProjectionBytes || !strings.Contains(index, "instruction_ref") || !strings.Contains(index, "result_ref") {
		t.Fatalf("unbounded recovery catalog: %d bytes", len(index))
	}
}
