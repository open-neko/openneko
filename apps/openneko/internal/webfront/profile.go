package webfront

import (
	"errors"
	"net/http"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"
)

// maxProfileField counts UTF-16 code units, as JavaScript's String.length does.
const maxProfileField = 200_000

type profileAnswer struct {
	BusinessProfile        string `json:"businessProfile"`
	IndustryInsights       string `json:"industryInsights"`
	CompanyNote            string `json:"companyNote"`
	IndustryInsightsStatus string `json:"industryInsightsStatus"`
}

func (a *API) profileGet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID, err := a.OrgID(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var business, insights, note *string
	var researchOff, building bool
	err = a.DB.QueryRow(ctx, `
		select p.business_profile, p.industry_insights, p.company_note,
		  coalesce((select not c.enabled or c.provider = 'disabled' from llm_provider_config c
		    where c.org_id = $1 and c.scope = 'research' and c.provider in ('disabled', 'perplexity') limit 1), false),
		  exists (select 1 from processing_job j where j.org_id = $1 and j.kind = 'industry_insights_build'
		    and j.status in ('queued', 'running'))
		from customer_profile p where p.org_id = $1 and p.is_current limit 1`, orgID).
		Scan(&business, &insights, &note, &researchOff, &building)
	if errors.Is(err, pgx.ErrNoRows) {
		sendError(w, http.StatusNotFound, "no profile")
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := profileAnswer{BusinessProfile: deref(business), IndustryInsights: deref(insights), CompanyNote: deref(note)}
	switch {
	case out.IndustryInsights != "":
		out.IndustryInsightsStatus = "ready"
	case researchOff:
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
