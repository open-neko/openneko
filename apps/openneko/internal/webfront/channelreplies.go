package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
)

const (
	replyInThread  = "thread"
	replyDirect    = "direct"
	replyListed    = "listed"
	maxReplyPlaces = 200
)

type replyPlaces struct {
	Mode   string   `json:"mode"`
	Places []string `json:"places"`
}

type channelReplyRules struct {
	Slack           replyPlaces `json:"slack"`
	Telegram        replyPlaces `json:"telegram"`
	KeptFromUpgrade bool        `json:"keptFromUpgrade"`
}

var replyPlacePatterns = map[string]*regexp.Regexp{
	"slack":    regexp.MustCompile(`^[CG][A-Z0-9]{1,20}$`),
	"telegram": regexp.MustCompile(`^(-[0-9]{1,20}|@[A-Za-z0-9_]{5,32})$`),
}

func newInstallReplyRules() channelReplyRules {
	return channelReplyRules{Slack: replyPlaces{replyListed, []string{}}, Telegram: replyPlaces{replyListed, []string{}}}
}

func (r *channelReplyRules) of(channel string) *replyPlaces {
	switch channel {
	case "slack":
		return &r.Slack
	case "telegram":
		return &r.Telegram
	}
	return nil
}

func replyRulesFrom(raw []byte) channelReplyRules {
	rules := newInstallReplyRules()
	_ = json.Unmarshal(raw, &rules)
	for _, p := range []*replyPlaces{&rules.Slack, &rules.Telegram} {
		if p.Mode != replyInThread && p.Mode != replyDirect {
			p.Mode = replyListed
		}
		if p.Places == nil {
			p.Places = []string{}
		}
	}
	return rules
}

func (a *API) channelReplyRules(ctx context.Context, orgID string) (channelReplyRules, error) {
	var raw []byte
	err := a.DB.QueryRow(ctx, `select config from llm_provider_config where org_id = $1 and scope = 'channel-replies'`, orgID).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return channelReplyRules{}, err
	}
	return replyRulesFrom(raw), nil
}

// sharedPlace names the group chat a recipient points at. A Slack direct
// message, an ephemeral reply and a Telegram private chat are not shared.
func sharedPlace(channel string, recipient any) (string, bool) {
	switch channel {
	case "slack":
		id, _ := jsGet(recipient, "channel").(string)
		if id == "" || strings.HasPrefix(id, "D") || jsGet(recipient, "ephemeral") == true {
			return "", false
		}
		return id, true
	case "telegram":
		switch v := jsGet(recipient, "chatId").(type) {
		case float64:
			return jsStringOf(v), v < 0
		case string:
			return v, strings.HasPrefix(v, "-") || strings.HasPrefix(v, "@")
		}
	}
	return "", false
}

func (p replyPlaces) private(place string) bool {
	switch p.Mode {
	case replyInThread:
		return false
	case replyDirect:
		return true
	}
	return !slices.Contains(p.Places, place)
}

// answersPrivately says whether a send to this recipient goes to one person
// in place of the group chat.
func (a *API) answersPrivately(ctx context.Context, orgID, channel string, recipient json.RawMessage) (bool, error) {
	parsed, err := jsParse(orEmptyObject(recipient))
	if err != nil {
		return false, err
	}
	place, shared := sharedPlace(channel, parsed)
	if !shared {
		return false, nil
	}
	rules, err := a.channelReplyRules(ctx, orgID)
	if err != nil {
		return false, err
	}
	return rules.of(channel).private(place), nil
}

// queueChannelSend queues a send under the org's reply rule. person is the
// channel user who may read it privately; with none, the group chat gets the
// note alone.
func (a *API) queueChannelSend(ctx context.Context, orgID, channel string, recipient json.RawMessage, events []any, key, notice, person string) error {
	ch := findBuiltInChannel(channel)
	private := false
	if ch != nil {
		var err error
		if private, err = a.answersPrivately(ctx, orgID, ch.ID, recipient); err != nil {
			return err
		}
	}
	if !private {
		return a.queueChannelDelivery(ctx, orgID, channel, recipient, events, key)
	}
	return a.queueChannelJob(ctx, jobs.ChannelDeliverArgs{OrgID: orgID, ChannelPlugin: channel, Recipient: recipient,
		Events: json.RawMessage(jsStringify(events, false)), Key: key, Notice: notice, PrivateTo: person})
}

// channelPersonOf is the linked identity of an OpenNeko user on a channel.
func (a *API) channelPersonOf(ctx context.Context, orgID, channel, userID string) (string, error) {
	if userID == "" {
		return "", nil
	}
	var id string
	err := a.DB.QueryRow(ctx, `select channel_user_id from channel_identity
		where org_id = $1 and channel_plugin = $2 and app_user_id = $3 and status = 'linked'
		order by updated_at desc limit 1`, orgID, channel, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

type channelRefusal struct{ error }

func refusedBy(err error) bool {
	var r channelRefusal
	return errors.As(err, &r)
}

func slackOpenDirect(ctx context.Context, person string, env map[string]string) (string, error) {
	token := env["SLACK_BOT_TOKEN"]
	if token == "" {
		return "dry-run-" + person, nil
	}
	raw, status, err := postChannelJSON(ctx, slackAPIBase+"/"+url.PathEscape("conversations.open"),
		map[string]string{"Authorization": "Bearer " + token}, jsObjOf("users", person))
	if err != nil {
		return "", fmt.Errorf("Slack conversations.open network error: %w", err)
	}
	var envelope struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return "", fmt.Errorf("Slack conversations.open returned non-JSON (HTTP %d)", status)
	}
	if !envelope.OK || envelope.Channel.ID == "" {
		return "", channelRefusal{fmt.Errorf("Slack conversations.open returned ok=false (%s)", envelope.Error)}
	}
	return envelope.Channel.ID, nil
}

func privateNote(channel, notice, person string, sent bool) string {
	mention := ""
	if channel == "slack" && notice == "reply" && person != "" {
		mention = "<@" + person + "> "
	}
	direct := "your direct messages"
	if channel == "telegram" {
		direct = "a private chat with you"
	}
	switch {
	case notice == "reply" && sent:
		return mention + "I sent the answer to " + direct + "."
	case notice == "reply" && channel == "telegram":
		return "I could not send the answer to a private chat with you. Open a chat with this bot, then ask again."
	case notice == "reply":
		return mention + "I could not send the answer to your direct messages. Open a direct message with the OpenNeko app, then ask again."
	case sent:
		return "A workflow output went to its owner privately."
	}
	return "A workflow output is ready in OpenNeko. Its owner can read it there."
}

// deliverPrivately sends to one person, then posts a note with no data in
// the group chat. A refused private send gives only the note.
func (a *API) deliverPrivately(ctx context.Context, ch *builtInChannel, job jobs.ChannelDeliverArgs, recipient any, events []any, env map[string]string) (channelDeliverResult, error) {
	sent := false
	if job.PrivateTo != "" {
		var to any
		var err error
		switch ch.ID {
		case "slack":
			var dm string
			if dm, err = slackOpenDirect(ctx, job.PrivateTo, env); err == nil {
				to = jsObjOf("channel", dm)
			}
		case "telegram":
			to = jsObjOf("chatId", job.PrivateTo)
		default:
			err = channelRefusal{fmt.Errorf("%s has no private chat", ch.ID)}
		}
		if err == nil {
			_, err = ch.Deliver(ctx, to, events, env)
		}
		switch {
		case err == nil:
			sent = true
		case refusedBy(err):
			log.Printf("[channel-delivery] %s: the private send was refused, so the group chat gets a note only: %v", ch.ID, err)
		default:
			return channelDeliverResult{}, err
		}
	}
	note := []any{jsObjOf("kind", "converse", "id", "note-"+job.Key, "role", "assistant", "text", privateNote(ch.ID, job.Notice, job.PrivateTo, sent))}
	result, err := ch.Deliver(ctx, recipient, note, env)
	if err != nil && sent {
		log.Printf("[channel-delivery] %s: the answer went privately; the note in the group chat failed: %v", ch.ID, err)
		return channelDeliverResult{Delivered: true}, nil
	}
	return result, err
}

func (a *API) channelRepliesGet(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	rules, err := a.channelReplyRules(r.Context(), act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, rules)
}

func (a *API) channelRepliesPut(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	body, _ := readBody(r)
	channel, _ := fieldString(body, "channel")
	mode, _ := fieldString(body, "mode")
	pattern := replyPlacePatterns[channel]
	if pattern == nil {
		sendError(w, http.StatusBadRequest, "channel must be slack or telegram")
		return
	}
	if mode != replyInThread && mode != replyDirect && mode != replyListed {
		sendError(w, http.StatusBadRequest, "mode must be thread, direct or listed")
		return
	}
	rawPlaces, _ := field(body, "places")
	list, isList := rawPlaces.([]any)
	if rawPlaces != nil && !isList || len(list) > maxReplyPlaces {
		sendError(w, http.StatusBadRequest, fmt.Sprintf("places must be a list of at most %d chat IDs", maxReplyPlaces))
		return
	}
	places := []string{}
	for _, v := range list {
		s, _ := v.(string)
		s = strings.TrimSpace(s)
		if !pattern.MatchString(s) {
			sendError(w, http.StatusBadRequest, fmt.Sprintf("%q is not a %s chat ID", s, channel))
			return
		}
		if !slices.Contains(places, s) {
			places = append(places, s)
		}
	}
	ctx := r.Context()
	var before, after replyPlaces
	var rules channelReplyRules
	err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('llm_provider_config:channel-replies:' || $1))`, act.OrgID); err != nil {
			return err
		}
		var raw []byte
		err := tx.QueryRow(ctx, `select config from llm_provider_config where org_id = $1 and scope = 'channel-replies'`, act.OrgID).Scan(&raw)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		rules = replyRulesFrom(raw)
		before = *rules.of(channel)
		after = replyPlaces{mode, places}
		if before.Mode == after.Mode && slices.Equal(before.Places, after.Places) {
			return nil
		}
		*rules.of(channel) = after
		rules.KeptFromUpgrade = false
		stored, _ := json.Marshal(rules)
		_, err = tx.Exec(ctx, `insert into llm_provider_config (org_id, scope, provider, enabled, config)
			values ($1, 'channel-replies', 'channel-replies', true, $2)
			on conflict (org_id, scope) do update set config = excluded.config, updated_at = now()`, act.OrgID, stored)
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if before.Mode != after.Mode || !slices.Equal(before.Places, after.Places) {
		a.recordAudit(ctx, auditEvent{OrgID: act.OrgID, EntityKind: "channel_replies", EntityID: channel, Event: "channel:replies_changed",
			Payload: map[string]any{"actorUserId": auditActor(act.UserID), "channel": channel,
				"mode": after.Mode, "places": stringsAny(after.Places), "previousMode": before.Mode, "previousPlaces": stringsAny(before.Places)}})
	}
	sendJSON(w, http.StatusOK, rules)
}
