package webfront

import (
	"context"
	"log"
)

func fieldString(body any, name string) (string, bool) {
	v, _ := field(body, name)
	s, ok := v.(string)
	return s, ok
}

type configChange struct {
	OrgID, Kind, Ref, Actor, Summary, Status, UserID, Scope string
}

// insertConfigChange adds one config_change audit row. A failure is logged
// only: the write that called it stands.
func (a *API) insertConfigChange(ctx context.Context, c configChange) {
	if c.Status == "" {
		c.Status = "recorded"
	}
	if c.Scope == "" {
		c.Scope = "team"
	}
	_, err := a.DB.Exec(ctx, `insert into config_change (org_id, artifact_kind, artifact_ref, scope, user_id, actor_user_id, summary, status)
		values ($1, $2, $3, $8, $7, nullif($4, ''), $5, $6)`,
		c.OrgID, c.Kind, c.Ref, c.Actor, c.Summary, c.Status, c.UserID, c.Scope)
	if err != nil {
		log.Printf("[config-change] config_change insert failed: %v", err)
	}
}

func (a *API) recordConfigChange(ctx context.Context, orgID, message, kind, ref, actorID string) {
	a.insertConfigChange(ctx, configChange{OrgID: orgID, Kind: kind, Ref: ref, Actor: actorID, Summary: message})
}
