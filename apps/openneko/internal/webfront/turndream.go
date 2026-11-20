package webfront

import (
	"context"
	"log"
	"strings"
)

// dreamLessonsPerTurn is the number of lessons a turn takes.
const dreamLessonsPerTurn = 5

type dreamLesson struct {
	ID, Kind, Shape, Body string
}

const dreamLessonKeyword = `ts_rank_cd(to_tsvector('english', question_shape),
	nullif(replace(plainto_tsquery('english', $2)::text, ' & ', ' | '), '')::tsquery)`

// dreamLessonsContext gives the active lessons of the org closest to the
// question, as prompt text, and records each one as offered to the run.
func (a *API) dreamLessonsContext(ctx context.Context, orgID, runID, query string) (string, error) {
	query = jsTrim(query)
	if query == "" {
		return "", nil
	}
	lessons, err := a.dreamLessonsFor(ctx, orgID, query, dreamLessonsPerTurn)
	if err != nil || len(lessons) == 0 {
		return "", err
	}
	ids := make([]string, len(lessons))
	lines := make([]string, len(lessons))
	for i, l := range lessons {
		ids[i] = l.ID
		lines[i] = "- [" + l.ID + "] " + l.Kind + " · " + l.Shape + "\n" + indentLines(l.Body, "  ")
	}
	if runID == "" {
		return strings.Join(lines, "\n"), nil
	}
	if _, err := a.DB.Exec(ctx, `with offered as (
		  insert into dream_lesson_use (run_id, lesson_id, org_id)
		  select $2::uuid, id, org_id from dream_lesson where org_id = $1 and id = any($3::uuid[])
		  on conflict do nothing returning lesson_id)
		update dream_lesson set use_count = use_count + 1, last_used_at = now()
		where id in (select lesson_id from offered)`, orgID, runID, ids); err != nil {
		return "", err
	}
	return strings.Join(lines, "\n"), nil
}

func (a *API) dreamLessonsFor(ctx context.Context, orgID, query string, limit int) ([]dreamLesson, error) {
	if model := a.EmbeddingModel(); model != "" {
		if err := a.embedDreamLessons(ctx, orgID, model); err != nil {
			log.Printf("[dreaming] lesson embedding failed; selecting by keyword: %v", err)
		} else if v, err := a.EmbedText(ctx, query); err != nil {
			log.Printf("[dreaming] embedding unavailable; selecting by keyword: %v", err)
		} else {
			return a.collectDreamLessons(ctx, `select id::text, kind, question_shape, body from dream_lesson
				where org_id = $1 and state = 'active' and embedding_model = $2
				order by embedding <=> $3::vector, updated_at desc limit $4`, orgID, model, vectorText(v), limit)
		}
	}
	return a.collectDreamLessons(ctx, `select id::text, kind, question_shape, body from dream_lesson
		where org_id = $1 and state = 'active'
		order by coalesce(`+dreamLessonKeyword+`, 0) desc, updated_at desc limit $3`, orgID, query, limit)
}

// embedDreamLessons embeds the active lessons that the configured model has
// not embedded yet.
func (a *API) embedDreamLessons(ctx context.Context, orgID, model string) error {
	pending, err := a.collectDreamLessons(ctx, `select id::text, kind, question_shape, body from dream_lesson
		where org_id = $1 and state = 'active' and (embedding is null or embedding_model is distinct from $2)
		order by created_at limit 200`, orgID, model)
	if err != nil {
		return err
	}
	for _, l := range pending {
		v, err := a.EmbedText(ctx, l.Shape)
		if err != nil {
			return err
		}
		if _, err := a.DB.Exec(ctx, `update dream_lesson set embedding = $3::vector, embedding_model = $2
			where id = $1::uuid`, l.ID, model, vectorText(v)); err != nil {
			return err
		}
	}
	return nil
}

func (a *API) collectDreamLessons(ctx context.Context, sql string, args ...any) ([]dreamLesson, error) {
	rows, err := a.DB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dreamLesson
	for rows.Next() {
		var l dreamLesson
		if err := rows.Scan(&l.ID, &l.Kind, &l.Shape, &l.Body); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func indentLines(text, prefix string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}
