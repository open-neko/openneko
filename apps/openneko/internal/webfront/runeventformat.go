package webfront

import (
	"context"
	"encoding/json"
	"net/http"
)

// A client that sends events=2 handles answer_reset and card_pending. An
// older bundle appends every assistant delta, so it gets the run as the
// mapper sent it before resets: the first attempt only.
const runEventFormatParam = "events"

func legacyRunEventClient(r *http.Request) bool {
	return r.URL.Query().Get(runEventFormatParam) != "2"
}

type legacyAnswerFilter struct{ reset bool }

func (f *legacyAnswerFilter) keep(typ, role string) bool {
	switch typ {
	case "answer_reset":
		f.reset = true
		return false
	case "card_pending":
		return false
	case "message":
		return role != "assistant" || !f.reset
	}
	return true
}

func (f *legacyAnswerFilter) keepPayload(payload []byte) bool {
	var head struct {
		Type string `json:"type"`
		Role string `json:"role"`
	}
	if json.Unmarshal(payload, &head) != nil {
		return true
	}
	return f.keep(head.Type, head.Role)
}

func (f *legacyAnswerFilter) keepEvent(event any) bool {
	typ, _ := jsGet(event, "type").(string)
	role, _ := jsGet(event, "role").(string)
	return f.keep(typ, role)
}

func (a *API) legacyAnswerFilterAt(ctx context.Context, orgID, runID string, afterID int64) (*legacyAnswerFilter, error) {
	f := &legacyAnswerFilter{}
	if afterID <= 0 {
		return f, nil
	}
	err := a.DB.QueryRow(ctx, `select exists (select 1 from work_run_event where org_id = $1 and run_id = $2 and kind = 'answer_reset' and id <= $3)`,
		orgID, runID, afterID).Scan(&f.reset)
	return f, err
}

func legacyBundleEvents(byRun map[string][]json.RawMessage) {
	for runID, events := range byRun {
		f := &legacyAnswerFilter{}
		kept := events[:0]
		for _, e := range events {
			if f.keepPayload(e) {
				kept = append(kept, e)
			}
		}
		byRun[runID] = kept
	}
}
