package webfront

import (
	"context"
	"errors"
	"log"
	"math"
	"os"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

// buildProfilerPrompt is buildProfilerPrompt() in packages/llm/src/profiler.ts.
func buildProfilerPrompt(orgName, companyNote string, k knowledgePack) string {
	tool := func(name string) string { return "mcp_neko_graphjin_" + name }
	queryTool := graphjinExecuteTool
	catalogTool, helpTool, savedQueryTool := tool("query_catalog"), tool("graphql_help"), tool("execute_saved_query")
	executeQuery := "call `" + queryTool + "` with {\"query\":\"<your read-only graphql>\"}"
	discovery := "1. Read the prefetched GraphJin knowledge sections below before writing any query. They are the authoritative DSL + schema/relationship context for this database. Don't run schema-discovery commands; that context is already prefetched here."
	if k.Mode == "agentic" {
		discovery = "1. The knowledge sections below are a SLIM role-aware bootstrap, not the whole schema. Start with `" + catalogTool + "` using {\"search\":\"<the profiling goal>\"}, then inspect the best returned ids for evidence, examples, and relationship edges. Use `" + helpTool + "` only when the catalog route is unclear. Prefer `" + savedQueryTool + "` when an approved query fits; never guess a field or relationship."
	}
	knowledge := "================================================================================\nTables — every table in the database (name, schema, column_count):\n================================================================================\n\n" +
		k.Tables +
		"\n\n================================================================================\nNamespaces — multi-database routing context:\n================================================================================\n\n" +
		k.Namespaces +
		"\n\n================================================================================\nInsights — hub tables, hot relationships, relationship paths, query templates, data-quality flags:\n================================================================================\n\n" +
		k.Insights +
		"\n\n================================================================================\nSyntax — authoritative GraphJin DSL reference (operators, aggregations, pagination):\n================================================================================\n\n" +
		k.Syntax
	if k.Mode == "agentic" {
		knowledge = "================================================================================\nTables visible to your role (deeper detail via gj_catalog on demand):\n================================================================================\n\n" +
			compactTableDigest(k.Tables) +
			"\n\n================================================================================\nHub tables — join paths and ready query templates (adapt, don't rediscover):\n================================================================================\n\n" +
			compactInsightsDigest(k.Insights) +
			"\n\n================================================================================\nHelp-card index — pull any card's full guidance with gj_catalog(id: \"help:<topic>\"):\n================================================================================\n\n" +
			compactHelpCardIndex(k.Insights) +
			"\n\n================================================================================\nQuery-DSL essentials — filters, query shape, aggregate patterns:\n================================================================================\n\n" +
			k.Syntax
	}
	return "You build a short markdown business profile about a customer company by querying its database via GraphJin.\n\nEXECUTION PATTERN:\n" +
		discovery +
		"\n2. Skim the tables + insights sections to identify what this business actually does (industry, offering, business model). Pick the handful of tables that matter.\n3. Run focused GraphQL queries to gather facts: main business event (date range, recent volume + value), top categories / products / services, geography, who is served, who does the work.\n4. Run queries by " +
		executeQuery +
		".\n5. If a response contains an \"errors\" array, read the GraphQL error and any errors[].extensions.graphjin_repair hint, then correct the query yourself.\n6. When you have enough facts, emit the final markdown body exactly per the OUTPUT FORMAT. No prose around it, no code fences.\n\nCOMPLETION CONTRACT:\n- Treat the requested output sections as an evidence checklist, not an invitation to exhaustively explore the schema.\n- A section is ready when you have representative queried evidence for it, or the available source does not expose it and the section can honestly say \"Not measured.\"\n- Stop querying and synthesize the profile as soon as every section is ready. Additional segmentations, alternative tables, or marginal precision that would not change the short profile are out of scope.\n- Do not get stuck repairing an optional fact. If a query cannot be corrected from the returned error and repair hint, use the evidence already collected and mark that fact \"Not measured.\"\n\nDATA ACCESS — READ-ONLY:\nUse only the caller-visible GraphJin MCP tools. The trusted host supplies their exact schemas and keeps the source URL and credential outside the sandbox. This profiler run is read-only: never submit a mutation, configuration change, or other state-changing operation even if a listed tool could perform one. DO NOT use `execute_code`, a shell, Python, raw HTTP requests, or any other path to talk to GraphJin.\n\n- Discover with `" +
		catalogTool +
		"` and inspect returned ids before querying data.\n- Execute data reads through `" +
		savedQueryTool +
		"` or `" +
		queryTool +
		"`.\n- Do not use configuration or write tools in this job.\n- For join planning, use catalog table cards, relationship rows, details_json, examples_json, and edges_json.\n- Do not use GraphJin dev tools or try to reach GraphJin outside its MCP surface.\n- Never invent data — every number in the profile must trace back to a GraphJin query-tool response from this run.\n\nQUERY CONSTRUCTION — let the database aggregate:\nPrefer one bulk query with server-side aggregation (count, sum, avg) over multiple round-trips that pull rows back to the agent. Specific GraphJin capabilities to reach for (consult `syntax.json` for full DSL reference):\n\n- Expression aggregates — sum(expr: {...}), ratio(expr: {...}) — USE THESE FIRST when a fact involves arithmetic across columns (e.g. SUM(price × qty)).\n- Joined-column access via dot-notation: { col: \"product.standardcost\" } works across FKs up to 3 hops.\n- For top-N by an aggregate, follow the prefetched syntax limitations. If GraphJin cannot order by an aggregate alias, fetch the grouped aggregate rows and sort the small result set in your reasoning.\n- Global single-row aggregate: a top-level select whose fields are ALL aggregates collapses to one row, no distinct needed.\n\nHARD CONSTRAINTS (violating any of these is a critical failure):\n- Never hardcode calendar years; compute periods with relative arithmetic. If you need an anchor date, use a recent date from the data.\n- For date/range filters, do not put multiple operators under the same column object. Use an explicit `and` array:\n  `where: { and: [{ orderdate: { gte: \"2024-06-30\" } }, { orderdate: { lte: \"2025-06-29\" } }] }`\n  not `where: { orderdate: { gte: \"...\", lte: \"...\" } }`.\n- Never use a bare limit without pagination. Use cursor-based pagination to process all rows, or use GraphQL aggregation with distinct to let the database aggregate.\n- Watch the silent 20-row default limit on every query level (top AND nested) — set explicit limit or use distinct+aggregation.\n- Never invent or interpolate. If a query returned no rows, the answer is \"Not measured.\", not a guess.\n\n" +
		knowledge + "\n\n" + profilerOutput(orgName, companyNote)
}

func profilerOutput(orgName, companyNote string) string {
	input := jsObjOf("companyName", orgName, "companyNote", companyNote)
	return "================================================================================\nOUTPUT FORMAT — respond with EXACTLY this markdown body, no code fences, no prose around it:\n================================================================================\n\n# " +
		orgName +
		" — Business Profile\n\n## What they are\n1–2 sentences: industry, offering, business model.\n\n## Who they serve\nRecipients in the business's own terms (customers, patients, members, accounts, …) with counts.\n\n## Where they operate\nGeographic / facility footprint with real names.\n\n## Scale\n- Date range covered, recent volume + value\n- Top revenue drivers\n- People served and people who do the work\n\n## Operational footprint\nBusiness functions the data represents.\n\n## What a downstream LLM should hold in mind\n3–5 non-obvious facts about the company's shape.\n\nRules for the markdown:\n- Every number must come from a query you actually ran.\n- Use human-readable names from the data, never bare IDs.\n- Match vocabulary to the business (a hospital has patients, not customers).\n- No meta talk about databases, schemas, queries, or the dataset.\n- If a fact isn't queryable, write \"Not measured.\"\n- Begin your response with the H1 `# " +
		orgName +
		" — Business Profile`. NOTHING may precede it — no preamble, no code fence, no acknowledgement.\n\n================================================================================\nINPUT — the company you must profile:\n================================================================================\n\n" +
		jsStringifyIndent(input, "  ") + "\n"
}

var (
	markdownFence  = jsRe("^```(?:markdown|md)?\\s*((?s:.*?))\\s*```\\s*$")
	profileFailure = regexp.MustCompile(`(?i)\b(i am sorry|unable to connect|restricted network|business_profile\.md|execute the graphql query yourself|graphjin server|could not access|couldn't access|cannot access|no direct access)\b`)
)

// validateBusinessProfile is validateBusinessProfile() in profiler.ts.
func validateBusinessProfile(raw string) (string, error) {
	profile := jsTrim(raw)
	if m := markdownFence.FindStringSubmatch(profile); m != nil {
		profile = m[1]
	}
	profile = jsTrim(profile)
	if profile == "" {
		return "", errors.New("profiler returned an empty business profile")
	}
	if profileFailure.MatchString(profile) {
		return "", errors.New("profiler returned failure text instead of a business profile")
	}
	return profile, nil
}

func profilerTimeoutMs() int {
	if n, ok := jsNumber(os.Getenv("OPENNEKO_PROFILER_TIMEOUT_MS")); ok && n > 0 && !math.IsInf(n, 0) {
		return int(n)
	}
	return agentLimitsFor("chat").TimeoutMs
}

// buildBusinessProfile is the business_profile_build job: the profiler
// agent reads the data source and writes a new current profile version.
// Industry research follows when the org has it on.
func (a *API) buildBusinessProfile(ctx context.Context, jobID, orgID string) error {
	var orgName, companyNote string
	var mcpURL *string
	err := a.DB.QueryRow(ctx, `select coalesce((select name from organization where id = $1), $1),
		(select mcp_url from data_source where org_id = $1 order by is_default desc, created_at limit 1),
		coalesce((select company_note from onboarding_wizard where org_id = $1 limit 1), '')`, orgID).Scan(&orgName, &mcpURL, &companyNote)
	if err != nil {
		return err
	}
	if mcpURL == nil || *mcpURL == "" {
		return errors.New("no mcp_url configured for org " + orgID)
	}
	a.jobProgress(ctx, jobID, "Reading data source")
	if err := a.ensureHostConfigProvisioned(ctx, orgID); err != nil {
		return err
	}
	roots, err := orgWorkspace(orgID)
	if err != nil {
		return err
	}
	if r := a.prefetchKnowledge(ctx, orgID, roots.Knowledge, false); r.OK {
		total := 0
		for _, f := range r.Files {
			total += f.Bytes
		}
		log.Printf("[profiler] org=%s knowledge refreshed (%d files, %dB)", orgID, len(r.Files), total)
	} else {
		log.Printf("[profiler] org=%s knowledge refresh failed (%s); proceeding with on-disk pack", orgID, r.Error)
	}
	prompt := buildProfilerPrompt(orgName, companyNote, readKnowledgePack(roots.Knowledge))
	a.jobProgress(ctx, jobID, "Running profiler agent…")
	started := time.Now()
	var profile string
	_, err = a.runAgentJob(ctx, agentJob{OrgID: orgID, RunID: jobID, Label: "profiler", Prompt: prompt, GraphjinRead: true,
		TimeoutMs: profilerTimeoutMs(), MaxAttempts: 3, Validate: func(text string) error {
			var verr error
			profile, verr = validateBusinessProfile(text)
			return verr
		}})
	if err != nil {
		return err
	}
	log.Printf("[profiler] org=%s done in %.0fs (%d chars)", orgID, time.Since(started).Seconds(), jsLength(profile))
	a.jobProgress(ctx, jobID, "Profile drafted")
	a.jobProgress(ctx, jobID, "Saving profile")
	research, err := llm.ResearchFor(ctx, a.DB, a.SecretsDir, orgID)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `update customer_profile set is_current = false where org_id = $1 and is_current`, orgID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into customer_profile (org_id, version, is_current, company_note, business_profile, built_by_job)
			values ($1, coalesce((select max(version) from customer_profile where org_id = $1), 0) + 1, true, $2, $3, $4)`,
			orgID, companyNote, profile, jobID); err != nil {
			return err
		}
		if research.Off() {
			log.Printf("[business_profile_build] research disabled for org %s; skipping industry_insights_build", orgID)
			return nil
		}
		next, err := a.queueBuildJob(ctx, tx, jobs.KindIndustryInsightsBuild, orgID, "chain_after_business_profile_build")
		if err == nil {
			log.Printf("[business_profile_build] chained industry_insights_build job %s for org %s", next, orgID)
		}
		return err
	})
}
