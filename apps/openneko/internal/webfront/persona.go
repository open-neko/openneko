package webfront

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

type operatorProfile struct {
	ID           string     `json:"id"`
	OrgID        string     `json:"orgId"`
	UserID       string     `json:"userId"`
	DisplayName  *string    `json:"displayName"`
	RoleTemplate string     `json:"roleTemplate"`
	FocusAreas   []string   `json:"focusAreas"`
	Answers      storedJSON `json:"answers"`
	BriefMd      string     `json:"briefMd"`
}

const operatorProfileColumns = `id::text, org_id, user_id, display_name, role_template, focus_areas, answers, brief_md`

func scanOperatorProfile(row pgx.Row) (*operatorProfile, error) {
	var p operatorProfile
	err := row.Scan(&p.ID, &p.OrgID, &p.UserID, &p.DisplayName, &p.RoleTemplate, &p.FocusAreas, (*[]byte)(&p.Answers), &p.BriefMd)
	if err != nil {
		return nil, err
	}
	if p.FocusAreas == nil {
		p.FocusAreas = []string{}
	}
	return &p, nil
}

func (a *API) operatorProfile(ctx context.Context, orgID, userID string) (*operatorProfile, error) {
	p, err := scanOperatorProfile(a.DB.QueryRow(ctx, `select `+operatorProfileColumns+` from operator_profile
		where org_id = $1 and user_id in ($2, '') order by user_id = '' limit 1`, orgID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// operatorProfileSection is buildOperatorProfileSection() in
// packages/llm/src/work/personas.ts.
func operatorProfileSection(p *operatorProfile) string {
	if p == nil {
		return ""
	}
	var lines []string
	if p.DisplayName != nil && *p.DisplayName != "" {
		lines = append(lines, "You are working for "+*p.DisplayName+".")
	}
	if p.RoleTemplate != "" {
		lines = append(lines, "Their role: "+p.RoleTemplate+".")
	}
	if len(p.FocusAreas) > 0 {
		lines = append(lines, "They care most about:")
		for _, f := range p.FocusAreas {
			lines = append(lines, "- "+f)
		}
	}
	if brief := jsTrim(p.BriefMd); brief != "" {
		lines = append(lines, "", brief)
	}
	if len(lines) == 0 {
		return ""
	}
	return "<operator-profile>\n" + strings.Join(lines, "\n") + "\n</operator-profile>"
}

func sendPersona(w http.ResponseWriter, p *operatorProfile) {
	sendJSON(w, http.StatusOK, struct {
		Profile       *operatorProfile `json:"profile"`
		PromptPreview string           `json:"promptPreview"`
	}{p, operatorProfileSection(p)})
}

func (a *API) personaGet(w http.ResponseWriter, r *http.Request) {
	act, err := a.currentActor(r.Context(), r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	p, err := a.operatorProfile(r.Context(), act.OrgID, act.UserID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendPersona(w, p)
}

func (a *API) personaPut(w http.ResponseWriter, r *http.Request) {
	act, err := a.currentActor(r.Context(), r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	var body any = &jsObj{vals: map[string]any{}}
	if err == nil {
		if v, perr := jsParse(raw); perr == nil {
			body = v
		}
	}
	if body == nil {
		sendError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	vals := map[string]any{}
	if o, ok := body.(*jsObj); ok {
		vals = o.vals
	}

	target := act.UserID
	if s, ok := vals["userId"].(string); ok {
		target = s
	}
	if !act.Admin && (target == "" || target != act.UserID) {
		who := "member"
		if act.UserID != "" {
			who += " " + act.UserID
		}
		what := target
		if what == "" {
			what = "(org default)"
		}
		sendError(w, http.StatusForbidden, who+" may not write persona "+what)
		return
	}

	var sets []string
	args := []any{act.OrgID, target, nil, "", []string{}, "{}", ""}
	if dn, present := vals["displayName"]; present {
		if s, ok := dn.(string); ok {
			args[2] = s
			sets = append(sets, "display_name = excluded.display_name")
		} else if dn == nil {
			sets = append(sets, "display_name = excluded.display_name")
		}
	}
	if s, ok := vals["roleTemplate"].(string); ok {
		args[3] = s
		sets = append(sets, "role_template = excluded.role_template")
	}
	if list, ok := vals["focusAreas"].([]any); ok {
		areas := []string{}
		for _, item := range list {
			if s, ok := item.(string); ok && jsTrim(s) != "" {
				areas = append(areas, s)
			}
		}
		args[4] = areas
		sets = append(sets, "focus_areas = excluded.focus_areas")
	}
	switch v := vals["answers"].(type) {
	case *jsObj, []any:
		args[5] = jsStringify(v, false)
		sets = append(sets, "answers = excluded.answers")
	}
	if s, ok := vals["briefMd"].(string); ok {
		args[6] = s
		sets = append(sets, "brief_md = excluded.brief_md")
	}
	sets = append(sets, "updated_at = now()")
	p, err := scanOperatorProfile(a.DB.QueryRow(r.Context(), `insert into operator_profile
		(org_id, user_id, display_name, role_template, focus_areas, answers, brief_md)
		values ($1, $2, $3, $4, $5, $6::jsonb, $7)
		on conflict (org_id, user_id) do update set `+strings.Join(sets, ", ")+`
		returning `+operatorProfileColumns, args...))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendPersona(w, p)
}
