package webfront

import (
	"context"
	"log"
	"slices"
	"strings"
)

var (
	coreGlobalKinds = map[string]bool{"preference": true, "business_rule": true, "metric_definition": true, "company_context": true}
	coreThreadKinds = map[string]bool{"business_rule": true, "metric_definition": true, "thread_note": true, "correction": true}
)

// memoryScope is the WorkMemoryContext of a turn. Layer is the member's own
// layer, or "" for the team layer. Team is nil when the actor holds every
// team memory.
type memoryScope struct {
	OrgID, ThreadID, RunID, Layer string
	Team                          []string
}

const memoryTeamFilter = `($3::text[] is null or scope = 'thread'
	or (scope = 'global' and 'global' = any($3::text[]))
	or (scope = 'database' and ('database:' || coalesce(scope_id, '')) = any($3::text[])))`

const memoryVisible = `org_id = $1 and archived_at is null and (
	($2::text is null and user_id is null and ` + memoryTeamFilter + `)
	or ($2::text is not null and ((user_id = $2 and suppressed = false)
	  or (user_id is null and ` + memoryTeamFilter + ` and not exists (
	    select 1 from work_memory p where p.org_id = work_memory.org_id and p.user_id = $2
	      and p.overrides_origin_id = work_memory.origin_id and p.archived_at is null)))))`

func (s memoryScope) args() []any {
	var layer *string
	if s.Layer != "" {
		layer = &s.Layer
	}
	var team any
	if s.Team != nil {
		team = s.Team
	}
	return []any{s.OrgID, layer, team}
}

func keptIntact(memories []memoryOut) []memoryOut {
	return slices.DeleteFunc(memories, func(m memoryOut) bool {
		if !m.IntegrityOK {
			log.Printf("[work-memory] integrity check FAILED for memory %s (org %s) — excluded from agent context", m.ID, m.OrgID)
		}
		return !m.IntegrityOK
	})
}

// memoryPromptContext is formatWorkMemoryPromptContext() in
// packages/llm/src/work/memory.ts with the turn's options: the message as
// the context query and a limit of 5.
func (a *API) memoryPromptContext(ctx context.Context, s memoryScope, query string) (string, error) {
	all, err := a.collectMemories(ctx, a.DB, `select `+memoryColumns+` from work_memory where `+memoryVisible+`
		order by pinned desc, updated_at desc limit 500`, s.args()...)
	if err != nil {
		return "", err
	}
	var merged []memoryOut
	for _, m := range keptIntact(all) {
		if m.Pinned || m.Scope == "global" && coreGlobalKinds[m.Kind] ||
			m.Scope == "thread" && deref(m.ScopeID) == s.ThreadID && coreThreadKinds[m.Kind] {
			merged = append(merged, m)
		}
	}
	merged = merged[:min(len(merged), 16)]
	seen := map[string]bool{}
	for _, m := range merged {
		seen[m.ID] = true
	}
	if jsTrim(query) != "" {
		found, err := a.memoriesByContext(ctx, s, jsTrim(query), 5)
		if err != nil {
			return "", err
		}
		for _, m := range found {
			if !seen[m.ID] {
				merged = append(merged, m)
			}
		}
	}
	if len(merged) == 0 {
		return "No memories are currently saved for this workspace or thread.", nil
	}
	lines := []string{"Memories the operator has saved — treat as durable context:"}
	for _, m := range merged {
		scope := m.Scope
		if m.ScopeID != nil && *m.ScopeID != "" {
			scope += ":" + *m.ScopeID
		}
		lines = append(lines, "- ["+m.ID+"] "+m.Kind+" ("+scope+"): "+m.Text)
	}
	return strings.Join(lines, "\n"), nil
}

const memoryKeywordQuery = `nullif(replace(plainto_tsquery('english', $4)::text, ' & ', ' | '), '')::tsquery`

// memoriesByContext is searchWorkMemoryByContext().
func (a *API) memoriesByContext(ctx context.Context, s memoryScope, query string, limit int) ([]memoryOut, error) {
	model := a.EmbeddingModel()
	var vector string
	if model != "" {
		v, err := a.EmbedText(ctx, query)
		if err != nil {
			log.Printf("[work-memory] embedding unavailable; searching by keyword: %v", err)
		} else {
			vector = vectorText(v)
		}
	}
	keyword := func(unindexed string) ([]memoryOut, error) {
		args := append(s.args(), query, limit)
		filter := ""
		if unindexed != "" {
			args = append(args, unindexed)
			filter = ` and (embedding is null or embedding_model is distinct from $6)`
		}
		return a.collectMemories(ctx, a.DB, `select `+memoryColumns+` from work_memory
			where `+memoryVisible+filter+` and to_tsvector('english', text) @@ `+memoryKeywordQuery+`
			order by ts_rank_cd(to_tsvector('english', text), `+memoryKeywordQuery+`) desc, updated_at desc limit $5`, args...)
	}
	var rows []memoryOut
	var err error
	if vector != "" {
		semantic, err := a.collectMemories(ctx, a.DB, `select `+memoryColumns+` from work_memory
			where `+memoryVisible+` and embedding is not null and embedding_model = $4
			order by embedding <=> $5::vector limit $6`, append(s.args(), model, vector, limit)...)
		if err != nil {
			return nil, err
		}
		unindexed, err := keyword(model)
		if err != nil {
			return nil, err
		}
		rows = interleaveMemories(semantic, unindexed, limit)
	} else if rows, err = keyword(""); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if err := a.touchMemories(ctx, s, rows); err != nil {
		return nil, err
	}
	return keptIntact(rows), nil
}

func interleaveMemories(semantic, unindexed []memoryOut, limit int) []memoryOut {
	if len(unindexed) == 0 {
		return semantic[:min(len(semantic), limit)]
	}
	var out []memoryOut
	seen := map[string]bool{}
	push := func(list []memoryOut, i int) {
		if i >= len(list) || len(out) >= limit || seen[list[i].ID] {
			return
		}
		seen[list[i].ID] = true
		out = append(out, list[i])
	}
	for i := 0; i < max(len(semantic), len(unindexed)) && len(out) < limit; i++ {
		push(semantic, i)
		push(unindexed, i)
	}
	return out
}

func (a *API) touchMemories(ctx context.Context, s memoryScope, rows []memoryOut) error {
	var runID *string
	if s.RunID != "" {
		runID = &s.RunID
	}
	done := map[string]bool{}
	for _, m := range rows {
		if done[m.ID] {
			continue
		}
		done[m.ID] = true
		if _, err := a.DB.Exec(ctx, `update work_memory set use_count = use_count + 1, last_used_at = $3
			where org_id = $1 and id = $2`, s.OrgID, m.ID, jsNow()); err != nil {
			return err
		}
		id := m.ID
		if err := memoryEvent(ctx, a.DB, s.OrgID, &id, runID, nil, "search_hit", map[string]any{}); err != nil {
			return err
		}
	}
	return nil
}

func vectorText(v []float64) string {
	parts := make([]string, len(v))
	for i, f := range v {
		parts[i] = jsNumberText(f)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
