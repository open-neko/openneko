package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"unicode/utf8"

	ax "github.com/ax-llm/ax/packages/go"
)

const inlineSavedResultBytes = 4096
const inlineSavedInstructionBytes = 1024
const maxRecoveryProjectionBytes = 32768

// recoveryProjection is only a model-context view. The authoritative saved
// operations remain in the checkpoint and can be read by ID from this run's
// runtime. A large result is never copied into the initial resumed prompt.
func recoveryProjection(operations []SavedOperation) string {
	rows := make([]map[string]any, 0, len(operations))
	for _, op := range operations {
		row := map[string]any{"id": op.ID, "tool": op.Name(), "finished": op.Finished}
		if len(op.Instruction) <= inlineSavedInstructionBytes {
			row["instruction"] = op.Instruction
		} else {
			row["instruction_ref"] = savedReference(op.ID, []byte(op.Instruction))
			row["instruction_bytes"] = len(op.Instruction)
			row["instruction_preview"] = safePrefix(op.Instruction, 256)
		}
		if op.Error != "" {
			row["error"] = op.Error
		} else if len(op.Result) <= inlineSavedResultBytes {
			row["result"] = json.RawMessage(op.Result)
		} else {
			row["result_ref"] = savedReference(op.ID, op.Result)
			row["result_bytes"] = len(op.Result)
			row["result_preview"] = safePrefix(string(op.Result), 256)
		}
		rows = append(rows, row)
	}
	encoded, _ := json.Marshal(rows)
	if len(encoded) > maxRecoveryProjectionBytes {
		for i, op := range operations {
			if _, ok := rows[i]["result"]; ok {
				delete(rows[i], "result")
				rows[i]["result_ref"] = savedReference(op.ID, op.Result)
				rows[i]["result_bytes"] = len(op.Result)
				rows[i]["result_preview"] = safePrefix(string(op.Result), 256)
			}
			if _, ok := rows[i]["instruction"]; ok && len(op.Instruction) > 128 {
				delete(rows[i], "instruction")
				rows[i]["instruction_ref"] = savedReference(op.ID, []byte(op.Instruction))
				rows[i]["instruction_bytes"] = len(op.Instruction)
				rows[i]["instruction_preview"] = safePrefix(op.Instruction, 128)
			}
		}
		encoded, _ = json.Marshal(rows)
	}
	if len(encoded) > maxRecoveryProjectionBytes {
		for i, op := range operations {
			delete(rows[i], "instruction")
			delete(rows[i], "instruction_preview")
			delete(rows[i], "result_preview")
			rows[i]["instruction_ref"] = savedReference(op.ID, []byte(op.Instruction))
			rows[i]["instruction_bytes"] = len(op.Instruction)
		}
		encoded, _ = json.Marshal(rows)
	}
	return string(encoded)
}

func savedReference(id int, data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("saved:%d:%s", id, hex.EncodeToString(sum[:]))
}

func safePrefix(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

// savedOperationValue returns one prior operation only. Model-chosen IDs have
// no filesystem, tenant or current-operation access; this closure is bound to
// the validated checkpoint for the active run.
func savedOperationValue(operations []SavedOperation, value ax.Value) (ax.Value, error) {
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || number < 1 || number > float64(len(operations)) || math.Trunc(number) != number {
		return nil, fmt.Errorf("saved operation unavailable")
	}
	op := operations[int(number)-1]
	if op.ID != int(number) || !op.Finished {
		return nil, fmt.Errorf("saved operation unavailable")
	}
	encoded, err := json.Marshal(op)
	if err != nil {
		return nil, fmt.Errorf("saved operation unavailable")
	}
	var result ax.Value
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, fmt.Errorf("saved operation unavailable")
	}
	return result, nil
}
