package webfront

import (
	"context"
	"log"
	"regexp"
	"strings"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

const (
	sweptBotNameMax   = 24
	botNameMessages   = 6
	botNameMessageMax = 400
	botNameBatch      = 20
)

var botNamer = llm.Program{
	Signature:   `chatTitle:string "the chat title the bot carries now", instructions?:string "the bot's job description, absent when it has none", operatorMessages:string "the operator's first messages in the chat" -> name:string "the new bot name, 1 to 3 words"`,
	Instruction: `You name a chat bot. Read the chat title, the bot's job description and the operator's first messages. Write a short, friendly name of 1 to 3 words that says what the bot does, such as "Stock watcher" or "Weekly sales". Use sentence case. Use no quotes, emoji or end punctuation.`,
}

var (
	botNameMarks = regexp.MustCompile("[\"`*]")
	botNameEnd   = regexp.MustCompile(`[.!?:;,]+$`)
)

func cleanBotName(raw string) string {
	s := jsTrim(jsWhitespaceRun.ReplaceAllString(botNameMarks.ReplaceAllString(raw, ""), " "))
	s = strings.TrimRight(strings.TrimLeft(s, "'"), "'")
	return jsTrim(jsSlice(botNameEnd.ReplaceAllString(s, ""), sweptBotNameMax))
}

type botNameInput struct{ OrgID, ChatTitle, Instructions, OperatorMessages string }

func (a *API) nameBot(ctx context.Context, in botNameInput) (string, error) {
	values := map[string]any{"chatTitle": in.ChatTitle, "operatorMessages": in.OperatorMessages}
	if in.Instructions != "" {
		values["instructions"] = in.Instructions
	}
	res, err := a.hostCalls().Generate(ctx, in.OrgID, "bot_names", botNamer, values)
	if err != nil {
		return "", err
	}
	return jsValueString(res.Outputs["name"]), nil
}

// BotNameSweep gives each bot that carries its chat title a short name. Each
// bot gets one attempt.
func (a *API) BotNameSweep(ctx context.Context) error {
	named, failed, err := a.nameBackfilledBots(ctx, a.nameBot)
	if named > 0 || failed > 0 {
		log.Printf("[bot-names] named %d bot(s), %d kept their chat title", named, failed)
	}
	return err
}

func (a *API) nameBackfilledBots(ctx context.Context, name func(context.Context, botNameInput) (string, error)) (named, failed int, err error) {
	type pending struct {
		id, orgID, name, instructions string
		threadID                      *string
	}
	rows, err := a.DB.Query(ctx, `select id::text, org_id, name, instructions, thread_id::text from bot where name_pending = true limit $1`, botNameBatch)
	if err != nil {
		return 0, 0, err
	}
	var list []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.orgID, &p.name, &p.instructions, &p.threadID); err != nil {
			rows.Close()
			return 0, 0, err
		}
		list = append(list, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	for _, p := range list {
		next, err := a.botName(ctx, p.orgID, p.name, p.instructions, p.threadID, name)
		if err != nil {
			failed++
			log.Printf("[bot-names] bot %s keeps its chat title: %v", p.id, err)
		}
		var updated bool
		if err := a.DB.QueryRow(ctx, `with u as (update bot set name = case when $2 <> '' then $2 else name end,
			updated_at = case when $2 <> '' then now() else updated_at end, name_pending = false
			where id = $1::uuid and name_pending = true returning 1) select count(*) > 0 from u`, p.id, next).Scan(&updated); err != nil {
			return named, failed, err
		}
		if next != "" && updated {
			named++
			if p.threadID != nil {
				if _, err := a.DB.Exec(ctx, `update work_thread set title = $2 where id = $1::uuid`, *p.threadID, next); err != nil {
					return named, failed, err
				}
			}
		}
	}
	return named, failed, nil
}

func (a *API) botName(ctx context.Context, orgID, title, instructions string, threadID *string, name func(context.Context, botNameInput) (string, error)) (string, error) {
	if threadID == nil {
		return "", nil
	}
	rows, err := a.DB.Query(ctx, `select content from work_message where thread_id = $1::uuid and role = 'user'
		order by created_at asc limit $2`, *threadID, botNameMessages)
	if err != nil {
		return "", err
	}
	var lines []string
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			rows.Close()
			return "", err
		}
		if m := jsSlice(jsTrim(content), botNameMessageMax); m != "" {
			lines = append(lines, "- "+m)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(lines) == 0 {
		return "", err
	}
	raw, err := name(ctx, botNameInput{OrgID: orgID, ChatTitle: title, Instructions: jsTrim(instructions), OperatorMessages: strings.Join(lines, "\n")})
	if err != nil {
		return "", err
	}
	return cleanBotName(raw), nil
}
