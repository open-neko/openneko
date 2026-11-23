package webfront

import (
	"context"
	"errors"
	"net/http"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

// maxProfileField counts UTF-16 code units, as JavaScript's String.length does.
const maxProfileField = 200_000

// researchOff matches the job's resolver: a missing research row means off.
// A row with no API key still counts as on, so the build job reports the fault.
func (a *API) researchOff(ctx context.Context, orgID string) (bool, error) {
	research, err := llm.ResearchFor(ctx, a.DB, a.SecretsDir, orgID)
	if err != nil && !errors.Is(err, llm.ErrResearchKey) {
		return false, err
	}
	return research.Off(), nil
}

type profileAnswer struct {
	BusinessProfile        string   `json:"businessProfile"`
	IndustryInsights       string   `json:"industryInsights"`
	CompanyNote            string   `json:"companyNote"`
	IndustryInsightsStatus string   `json:"industryInsightsStatus"`
	StarterQuestions       []string `json:"starterQuestions,omitempty"`
}

func (a *API) profileGet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID, err := a.OrgID(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var business, insights, note *string
	off, err := a.researchOff(ctx, orgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var building bool
	err = a.DB.QueryRow(ctx, `
		select p.business_profile, p.industry_insights, p.company_note,
		  exists (select 1 from processing_job j where j.org_id = $1 and j.kind = 'industry_insights_build'
		    and j.status in ('queued', 'running'))
		from customer_profile p where p.org_id = $1 and p.is_current limit 1`, orgID).
		Scan(&business, &insights, &note, &building)
	if errors.Is(err, pgx.ErrNoRows) {
		sendError(w, http.StatusNotFound, "no profile")
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := profileAnswer{BusinessProfile: deref(business), IndustryInsights: deref(insights), CompanyNote: deref(note)}
	if _, questions := splitProfileQuestions(out.BusinessProfile); len(questions) > 0 {
		out.StarterQuestions = questions[:min(len(questions), 3)]
	}
	switch {
	case out.IndustryInsights != "":
		out.IndustryInsightsStatus = "ready"
	case off:
		out.IndustryInsightsStatus = "disabled"
	case building:
		out.IndustryInsightsStatus = "processing"
	default:
		out.IndustryInsightsStatus = "pending"
	}
	sendJSON(w, http.StatusOK, out)
}

func (a *API) profilePatch(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(r)
	switch body.(type) {
	case map[string]any, []any:
	default:
		ok = false
	}
	if !ok {
		sendError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var business, insights *string
	for _, f := range []struct {
		name string
		dst  **string
	}{{"businessProfile", &business}, {"industryInsights", &insights}} {
		v, _ := field(body, f.name)
		s, isString := v.(string)
		if !isString {
			continue
		}
		if len(utf16.Encode([]rune(s))) > maxProfileField {
			sendError(w, http.StatusRequestEntityTooLarge, f.name+" too large")
			return
		}
		*f.dst = &s
	}
	if business == nil && insights == nil {
		sendError(w, http.StatusBadRequest, "no fields to update")
		return
	}
	ctx := r.Context()
	orgID, err := a.OrgID(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var newBusiness, newInsights *string
	err = a.DB.QueryRow(ctx, `
		update customer_profile set
		  business_profile = case when $2::boolean then $3 else business_profile end,
		  industry_insights = case when $4::boolean then $5 else industry_insights end,
		  updated_at = now()
		where org_id = $1 and is_current
		returning business_profile, industry_insights`,
		orgID, business != nil, deref(business), insights != nil, deref(insights)).Scan(&newBusiness, &newInsights)
	if errors.Is(err, pgx.ErrNoRows) {
		sendError(w, http.StatusNotFound, "no profile")
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]string{
		"businessProfile":  deref(newBusiness),
		"industryInsights": deref(newInsights),
	})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
