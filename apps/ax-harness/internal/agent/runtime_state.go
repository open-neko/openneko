package agent

import (
	"bytes"
	"encoding/json"
)

// RuntimeStateUpdate replaces the prior host state snapshot. The target is an
// explicit Ax stage path; the state is JSON data, never an authority grant.
type RuntimeStateUpdate struct {
	Target string          `json:"target"`
	State  json.RawMessage `json:"state"`
}

func (u RuntimeStateUpdate) Valid() bool {
	if u.Target != "root/executor" && u.Target != "root/responder" {
		return false
	}
	if len(u.State) < 2 || len(u.State) > 4096 || !json.Valid(u.State) {
		return false
	}
	return bytes.HasPrefix(bytes.TrimSpace(u.State), []byte("{"))
}
