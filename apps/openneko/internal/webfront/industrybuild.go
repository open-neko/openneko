package webfront

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

var researchMissionWriter = llm.Program{
	Signature:   "companyName:string, companyNote:string, businessProfile:string -> missionCharter:string",
	Instruction: "You are a McKinsey associate scoping an\nindustry briefing for a partner. You will read a business_profile describing a\nspecific company and write a single research charter that a deep-research agent\nwill execute.\n\nThe charter must:\n\n- Name the industry and sub-segments in plain language drawn from the\n  business_profile. Do not invent industries the profile does not support.\n- Name the geographies the company actually operates in (use the real country /\n  region / city names from the profile, not generic phrases like \"global\").\n- Use the served-entity vocabulary the company itself uses — patients, riders,\n  members, accounts, tenants, students, customers — whichever the profile\n  shows. Tell the researcher to keep that vocabulary throughout.\n- Name the business model implied by the profile (transactional, subscription,\n  asset-based, regulated, project-based, services, membership, etc.).\n- Tell the researcher to investigate ALL of these dimensions, in this order:\n  industry definition, market size & growth, competitive landscape (top\n  players in THIS company's actual geographies), structural trends, regulatory\n  environment, value chain economics, risks & disruption vectors.\n- Tell the researcher to ground every claim in current public sources, prefer\n  the most recent data available, and use the section headings:\n  \"Industry definition\", \"Market size & growth\", \"Competitive landscape\",\n  \"Structural trends\", \"Regulatory environment\", \"Value chain & economics\",\n  \"Risks & disruption vectors\", \"What a downstream LLM should hold in mind\".\n- Tell the researcher the briefing will be read by downstream LLMs FIRST and\n  executives SECOND. Both want to scan, not read. So the output must be\n  BULLET POINTS ONLY — every bullet a concrete number, named entity, date,\n  or specific claim with an inline citation. No prose paragraphs. No narrative\n  transitions. No \"Overall\" / \"In summary\" / \"It is worth noting\" sentences.\n  Target 8-15 bullets per section, total briefing length MUST NOT exceed\n  12,000 characters. Cut, do not pad.\n  Marketing fluff is forbidden.\n\nHard rules:\n- Output ONLY the charter as flowing prose. No headings, no bullets, no\n  preamble like \"Here is the charter:\".\n- 150-300 words.\n- Address the researcher in second person (\"Investigate...\", \"Cover...\",\n  \"Use the vocabulary...\"), not third person.\n- If the business_profile is thin or vague on a dimension, instruct the\n  researcher to use industry defaults appropriate to the named industry and\n  geographies — do NOT make up facts about the company itself.",
}

const deepResearchSystemPrompt = "You are a McKinsey-style industry analyst\nwriting for downstream LLMs FIRST and executives SECOND. Both want to scan,\nnot read.\n\nCRITICAL — response shape:\n- Your response IS the published briefing. It will be stored verbatim in a\n  database column and rendered to users as markdown.\n- Begin your response with the H1 heading the user specifies. NOTHING may\n  precede it — no preface, no \"Here is the briefing:\", no recap of the\n  mission, no acknowledgement.\n\nOutput rules — STRICT:\n- Use exactly the section headings (H2) the user requests, in the order\n  requested.\n- Each section is BULLET POINTS ONLY. No introductory paragraph. No transition\n  prose. No narrative.\n- Each bullet is one discrete fact: a concrete number, a named\n  company/regulator/geography, a date, or a specific claim. No filler. No\n  \"Overall,\" / \"In summary,\" / \"It is worth noting\" sentences.\n- Inline citations like [1] are encouraged on every bullet that makes a\n  factual claim.\n- Sub-bullets are allowed for grouping (e.g. by region or by player) but\n  keep nesting to one level deep.\n- Aim for 8-15 bullets per section. Total briefing length MUST NOT exceed\n  12,000 characters. Density beats length. Cut, do not pad.\n- Ground every claim in current public sources. Prefer the most recent data\n  available.\n- No marketing language, no hedging adjectives, no \"robust ecosystem\" /\n  \"vibrant market\" / \"compelling growth story\" type prose."

func deepResearchUserPrompt(orgName, missionCharter string) string {
	return "# Research mission\n\n" + missionCharter + "\n\n# Output\n\nProduce the briefing as markdown. Your FIRST character must be the `#` of\nthe H1 title below. Nothing may precede it — no preamble, no <think>\nblock, no recap of this mission, no acknowledgement of these instructions.\n\n# " + orgName + " — Industry Insights\n\nThen use the eight section headings (H2) listed in the mission, in the\norder given.\n\n# Format — STRICT\n\n- Do NOT emit <think>…</think> tags, planning notes, step counts, search\n  logs, or any meta-commentary. Output ONLY the finished briefing body.\n- Sections are H2 headings followed immediately by a bulleted list. NO\n  introductory paragraph under any heading. NO narrative prose. NO transition\n  sentences.\n- Each bullet = one discrete fact: a concrete number, a named\n  company/regulator/geography/standard, a date, or a specific claim with\n  inline citation.\n- Aim for 8-15 bullets per section. Total briefing length MUST NOT exceed\n  12,000 characters. Density beats length. Cut, do not pad.\n- Sub-bullets are allowed for grouping (e.g. region breakdowns under a\n  player, or per-country regulation), but keep nesting to one level only.\n- Under \"What a downstream LLM should hold in mind\", give 5-7 bullets each\n  naming a non-obvious industry-specific fact (with the number or named\n  entity that anchors it) that should color how this company's KPIs are\n  interpreted.\n- No preamble before the H1. No trailing summary. No \"Sources:\" appendix —\n  inline citations only.\n- No marketing language. No \"robust\" / \"vibrant\" / \"compelling\" /\n  \"exciting growth story\". Plain factual prose inside each bullet."
}

const deepResearchTimeout = 20 * time.Minute

var thinkBlock = regexp.MustCompile(`(?is)<think\b[^>]*>.*?</think>[` + jsSpaceClass + `]*`)

type researchFailure struct{ msg string }

func (e researchFailure) Error() string { return e.msg }

// deepResearch is runDeepResearch() in industry-researcher.ts: one
// sonar-deep-research call, tried once more on a 5xx, a 429 or a network
// fault.
func (a *API) deepResearch(ctx context.Context, userPrompt string, r llm.ResearchSettings) (string, error) {
	text, err := a.deepResearchOnce(ctx, userPrompt, r)
	var status researchStatusError
	if err != nil && ctx.Err() == nil {
		var failure researchFailure
		switch {
		case errors.As(err, &status) && status.transient:
			log.Printf("[industry] perplexity %d, retrying once: %s", status.code, jsSlice(status.body, 200))
		case errors.As(err, &status) || errors.As(err, &failure):
			return "", err
		default:
			log.Printf("[industry] perplexity network error, retrying once: %v", err)
		}
		return a.deepResearchOnce(ctx, userPrompt, r)
	}
	return text, err
}

type researchStatusError struct {
	code      int
	transient bool
	body, msg string
}

func (e researchStatusError) Error() string { return e.msg }

func (a *API) deepResearchOnce(ctx context.Context, userPrompt string, r llm.ResearchSettings) (string, error) {
	model := r.Model
	if model == "" {
		model = "sonar-deep-research"
	}
	body, err := json.Marshal(map[string]any{"model": model, "reasoning_effort": "medium", "messages": []map[string]string{
		{"role": "system", "content": deepResearchSystemPrompt}, {"role": "user", "content": userPrompt}}})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, deepResearchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.researchURL(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+r.Secrets["apiKey"])
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		text := strings.TrimPrefix(res.Status, fmt.Sprintf("%d ", res.StatusCode))
		return "", researchStatusError{code: res.StatusCode, transient: res.StatusCode >= 500 || res.StatusCode == 429, body: string(raw),
			msg: fmt.Sprintf("perplexity %d %s: %s", res.StatusCode, text, jsSlice(string(raw), 500))}
	}
	var data struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Citations []string `json:"citations"`
	}
	if err := json.NewDecoder(res.Body).Decode(&data); err != nil {
		return "", err
	}
	content := ""
	if len(data.Choices) > 0 {
		content = data.Choices[0].Message.Content
	}
	content = jsTrim(thinkBlock.ReplaceAllString(content, ""))
	if content == "" {
		return "", researchFailure{"perplexity returned empty content"}
	}
	if len(data.Citations) == 0 {
		return content, nil
	}
	refs := make([]string, len(data.Citations))
	for i, url := range data.Citations {
		refs[i] = fmt.Sprintf("[%d]: %s", i+1, url)
	}
	return content + "\n\n" + strings.Join(refs, "\n"), nil
}

// buildIndustryInsights is the industry_insights_build job: a mission from
// the business profile, then one deep research call. It writes the insights
// on the current profile row.
func (a *API) buildIndustryInsights(ctx context.Context, jobID, orgID string) error {
	var orgName string
	var profileID, businessProfile, companyNote *string
	err := a.DB.QueryRow(ctx, `select coalesce((select name from organization where id = $1), $1), p.id::text, p.business_profile, p.company_note
		from (select 1) one left join lateral (select id, business_profile, company_note from customer_profile
		  where org_id = $1 and is_current limit 1) p on true`, orgID).Scan(&orgName, &profileID, &businessProfile, &companyNote)
	if err != nil {
		return err
	}
	if profileID == nil {
		return errors.New("no current customer_profile for org " + orgID + " — run business_profile_build first")
	}
	if businessProfile == nil || *businessProfile == "" {
		return errors.New("customer_profile " + *profileID + " has no business_profile — cannot research industry")
	}
	a.jobProgress(ctx, jobID, "Loading business profile")
	if err := a.ensureHostConfigProvisioned(ctx, orgID); err != nil {
		return err
	}
	insights, mission, err := a.researchIndustry(ctx, jobID, orgID, orgName, deref(companyNote), *businessProfile)
	if err != nil {
		return err
	}
	a.jobProgress(ctx, jobID, "Saving industry insights")
	_, err = a.DB.Exec(ctx, `update customer_profile set industry_insights = nullif($2, ''), industry_insights_research_task = nullif($3, '')
		where id = $1`, *profileID, insights, mission)
	return err
}

func (a *API) researchIndustry(ctx context.Context, jobID, orgID, orgName, companyNote, businessProfile string) (string, string, error) {
	research, err := llm.ResearchFor(ctx, a.DB, a.SecretsDir, orgID)
	if err != nil || research.Off() {
		return "", "", err
	}
	a.jobProgress(ctx, jobID, "Drafting research mission")
	res, err := a.hostCalls().Generate(ctx, orgID, "research_mission", researchMissionWriter,
		map[string]any{"companyName": orgName, "companyNote": companyNote, "businessProfile": businessProfile})
	if err != nil {
		return "", "", err
	}
	mission := jsTrim(jsValueString(res.Outputs["missionCharter"]))
	if mission == "" {
		return "", "", errors.New("mission writer returned empty charter")
	}
	log.Printf("[industry] mission written (%d chars)", jsLength(mission))
	a.jobProgress(ctx, jobID, fmt.Sprintf("Mission ready (%d chars)", jsLength(mission)))
	a.jobProgress(ctx, jobID, "Running deep research (sonar-deep-research, may take minutes)")
	started := time.Now()
	log.Printf("[industry] sonar-deep-research call started")
	insights, err := a.deepResearch(ctx, deepResearchUserPrompt(orgName, mission), research)
	if err != nil {
		return "", "", err
	}
	elapsed := fmt.Sprintf("%.0f", time.Since(started).Seconds())
	log.Printf("[industry] sonar-deep-research completed in %ss (%d chars)", elapsed, jsLength(insights))
	a.jobProgress(ctx, jobID, "Deep research complete ("+elapsed+"s)")
	return insights, mission, nil
}
