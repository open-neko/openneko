package webfront

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// joinedParts are the ids of the text rows that one joined row replaced, in
// order, and the end of each row's text in the joined content.
type joinedParts struct {
	ids  []int64
	ends []int32
}

// rest returns the joined payload with only the text after the last part the
// client has; ok is false when the client has every part.
func (p joinedParts) rest(payload []byte, has func(int64) bool) (out []byte, ok bool, err error) {
	if len(p.ids) != len(p.ends) {
		return nil, false, fmt.Errorf("joined row has %d ids and %d ends", len(p.ids), len(p.ends))
	}
	from := -1
	for i, id := range p.ids {
		if has(id) {
			from = i
		}
	}
	if from < 0 {
		return payload, true, nil
	}
	if from == len(p.ids)-1 {
		return nil, false, nil
	}
	v, err := jsParse(payload)
	if err != nil {
		return nil, false, err
	}
	e, ok := v.(*jsObj)
	if !ok {
		return nil, false, fmt.Errorf("joined row is not an object")
	}
	content, _ := e.vals["content"].(string)
	cut := int(p.ends[from])
	if cut > len(content) {
		return nil, false, fmt.Errorf("joined row cut %d is past its text", cut)
	}
	return []byte(jsStringify(bufferedRunEvent(e, content[cut:]), false)), true, nil
}

type storedRunText struct {
	id      int64
	key     string
	head    string
	event   *jsObj
	content string
}

// joinRunText replaces each series of text rows of one stream that no other
// row of the run splits with one row. The joined row keeps the last row's id,
// so a client that has the whole series skips it, and a client that resumes
// inside the series gets the rest through joinedParts.
func (a *API) joinRunText(ctx context.Context, orgID, runID string) error {
	rows, err := a.DB.Query(ctx, `select id, case when kind in ('message', 'progress') and joined_ids is null then payload::text end
		from work_run_event where org_id = $1 and run_id = $2 order by id`, orgID, runID)
	if err != nil {
		return err
	}
	var series [][]storedRunText
	var cur []storedRunText
	end := func() {
		if len(cur) > 1 {
			series = append(series, cur)
		}
		cur = nil
	}
	var id int64
	var payload *string
	if _, err := pgx.ForEachRow(rows, []any{&id, &payload}, func() error {
		t, ok := storedText(id, payload)
		if !ok {
			end()
			return nil
		}
		if len(cur) > 0 && (cur[0].key != t.key || cur[0].head != t.head) {
			end()
		}
		cur = append(cur, t)
		return nil
	}); err != nil {
		return err
	}
	end()
	if len(series) == 0 {
		return nil
	}
	return pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		for _, s := range series {
			ids := make([]int64, len(s))
			ends := make([]int32, len(s))
			text := ""
			for i, t := range s {
				text += t.content
				ids[i], ends[i] = t.id, int32(len(text))
			}
			last := s[len(s)-1]
			joined := bufferedRunEvent(last.event, text)
			if _, err := tx.Exec(ctx, `update work_run_event set payload = $2::jsonb, joined_ids = $3, joined_ends = $4 where id = $1`,
				last.id, jsStringify(joined, false), ids, ends); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `delete from work_run_event where id = any($1)`, ids[:len(ids)-1]); err != nil {
				return err
			}
		}
		return nil
	})
}

func storedText(id int64, payload *string) (storedRunText, bool) {
	if payload == nil {
		return storedRunText{}, false
	}
	v, err := jsParse([]byte(*payload))
	if err != nil {
		return storedRunText{}, false
	}
	e, ok := v.(*jsObj)
	key := bufferedEventKey(v)
	if !ok || key == "" {
		return storedRunText{}, false
	}
	content := e.vals["content"].(string)
	head := bufferedRunEvent(e, "")
	return storedRunText{id: id, key: key, head: jsCanonical(head), event: e, content: content}, true
}
