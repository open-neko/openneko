package webfront

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// parsedInbound is ParsedInbound in packages/channels: the intents of one
// update, where to reply and who sent it.
type parsedInbound struct {
	Intents   []*jsObj
	Recipient *jsObj
	Sender    *jsObj
}

func (p parsedInbound) js() *jsObj {
	intents := make([]any, len(p.Intents))
	for i, it := range p.Intents {
		intents[i] = it
	}
	out := jsObjOf("intents", intents)
	if p.Recipient != nil {
		out.set("recipient", p.Recipient)
	}
	if p.Sender != nil {
		out.set("sender", p.Sender)
	}
	return out
}

// firstString is the `asStr(a) ?? asStr(b) ?? …` chain: the first value that
// is a string, empty or not.
func firstString(values ...any) (string, bool) {
	for _, v := range values {
		if s, ok := v.(string); ok {
			return s, true
		}
	}
	return "", false
}

func objOrNil(v any) *jsObj {
	o, _ := v.(*jsObj)
	return o
}

func setIfText(o *jsObj, key, value string) {
	if value != "" {
		o.set(key, value)
	}
}

// splitJSSpace is s.split(/\s+/).
func splitJSSpace(s string) []string {
	var out []string
	start, inSpace := 0, false
	for i, r := range s {
		switch {
		case jsSpace(r) && !inSpace:
			out = append(out, s[start:i])
			inSpace = true
		case !jsSpace(r) && inSpace:
			start, inSpace = i, false
		}
	}
	if inSpace {
		return append(out, "")
	}
	return append(out, s[start:])
}

var leadingMention = regexp.MustCompile(`^[` + jsSpaceClass + `]*<@[^>]+>[` + jsSpaceClass + `]*`)

func slackIsSlashCommand(p *jsObj) bool {
	c, ok := jsGet(p, "command").(string)
	return ok && c != ""
}

// parseSlackInbound is parseSlackInbound() in packages/channels/src/inbound/slack.ts.
func parseSlackInbound(raw any) []*jsObj {
	p := objOrNil(raw)
	if p == nil {
		return nil
	}
	if slackIsSlashCommand(p) {
		text, _ := jsGet(p, "text").(string)
		words := strings.FieldsFunc(jsTrim(text), jsSpace)
		command := ""
		if len(words) > 0 {
			command = words[0]
		} else {
			c, _ := jsGet(p, "command").(string)
			command = strings.TrimPrefix(c, "/")
		}
		if command == "" {
			return nil
		}
		args := &jsObj{}
		if len(words) > 0 {
			args = jsObjOf("text", strings.Join(words[1:], " "))
		}
		return []*jsObj{jsObjOf("kind", "invoke", "command", command, "args", args)}
	}
	if jsGet(p, "type") == "block_actions" {
		actions, ok := jsGet(p, "actions").([]any)
		if !ok {
			return nil
		}
		message := jsGet(p, "message")
		threadRef, hasRef := firstString(jsGet(message, "thread_ts"), jsGet(message, "ts"), jsGet(jsGet(p, "container"), "message_ts"))
		var intents []*jsObj
		for _, entry := range actions {
			action := objOrNil(entry)
			if action == nil {
				continue
			}
			actionID, _ := jsGet(action, "action_id").(string)
			switch {
			case actionID == "approve" || actionID == "reject":
				value, _ := jsGet(action, "value").(string)
				intents = append(intents, jsObjOf("kind", "decision", "decisionRef", value, "choice", actionID))
			case strings.HasPrefix(actionID, "select:"):
				label, ok := jsGet(jsGet(action, "text"), "text").(string)
				if !ok {
					label = actionID[7:]
				}
				it := jsObjOf("kind", "utterance", "text", label)
				if hasRef {
					it.set("threadRef", threadRef)
				}
				intents = append(intents, it)
			}
		}
		return intents
	}
	if jsGet(p, "type") != "event_callback" {
		return nil
	}
	event := objOrNil(jsGet(p, "event"))
	if event == nil || jsTruthy(jsMember(event, "bot_id")) {
		return nil
	}
	text, isText := jsGet(event, "text").(string)
	if jsGet(event, "type") == "app_mention" && isText {
		text = jsTrim(leadingMention.ReplaceAllString(text, ""))
		if text == "" {
			return nil
		}
		it := jsObjOf("kind", "utterance", "text", text)
		if ref, ok := firstString(jsGet(event, "thread_ts"), jsGet(event, "ts")); ok {
			it.set("threadRef", ref)
		}
		return []*jsObj{it}
	}
	if jsGet(event, "type") != "message" || !isText || jsTruthy(jsMember(event, "subtype")) {
		return nil
	}
	threadTS, inThread := jsGet(event, "thread_ts").(string)
	if jsGet(event, "channel_type") == "im" {
		it := jsObjOf("kind", "utterance", "text", text)
		if inThread {
			it.set("threadRef", threadTS)
		}
		return []*jsObj{it}
	}
	if threadTS != "" {
		return []*jsObj{jsObjOf("kind", "utterance", "text", text, "threadRef", threadTS)}
	}
	return nil
}

// slackRecipient is recipientFromSlackPayload().
func slackRecipient(raw any) *jsObj {
	p := objOrNil(raw)
	if p == nil {
		return nil
	}
	if slackIsSlashCommand(p) {
		channel, _ := jsGet(p, "channel_id").(string)
		if channel == "" {
			return nil
		}
		out := jsObjOf("kind", "slack", "channel", channel)
		if user, _ := jsGet(p, "user_id").(string); user != "" {
			out.set("user", user)
			out.set("ephemeral", true)
		}
		return out
	}
	event, message, container := jsGet(p, "event"), jsGet(p, "message"), jsGet(p, "container")
	var mentionTS any
	if jsGet(event, "type") == "app_mention" {
		mentionTS = jsGet(event, "ts")
	}
	threadTS, _ := firstString(jsGet(event, "thread_ts"), mentionTS, jsGet(message, "thread_ts"), jsGet(message, "ts"), jsGet(container, "message_ts"))
	channel, _ := firstString(jsGet(event, "channel"), jsGet(jsGet(p, "channel"), "id"), jsGet(container, "channel_id"))
	if channel == "" {
		return nil
	}
	out := jsObjOf("kind", "slack", "channel", channel)
	setIfText(out, "thread_ts", threadTS)
	if _, ok := jsGet(event, "thread_ts").(string); ok && jsGet(event, "type") == "message" && jsGet(event, "channel_type") != "im" {
		out.set("requireThread", true)
	}
	return out
}

// slackSender is senderFromSlackPayload().
func slackSender(raw any) *jsObj {
	p := objOrNil(raw)
	if p == nil {
		return nil
	}
	if slackIsSlashCommand(p) {
		id, _ := jsGet(p, "user_id").(string)
		if id == "" {
			return nil
		}
		out := jsObjOf("id", id)
		name, _ := jsGet(p, "user_name").(string)
		setIfText(out, "displayName", name)
		team, _ := jsGet(p, "team_id").(string)
		setIfText(out, "workspaceId", team)
		return out
	}
	event := jsGet(p, "event")
	if jsTruthy(jsMember(event, "bot_id")) {
		return nil
	}
	user := jsGet(p, "user")
	id, _ := firstString(jsGet(event, "user"), jsGet(user, "id"))
	if id == "" {
		return nil
	}
	workspace, _ := firstString(jsGet(p, "team_id"), jsGet(jsGet(p, "team"), "id"), jsGet(event, "team"))
	name, _ := firstString(jsGet(user, "name"), jsGet(user, "username"))
	out := jsObjOf("id", id)
	setIfText(out, "displayName", name)
	setIfText(out, "workspaceId", workspace)
	return out
}

func parseSlackUpdate(raw any, _ map[string]string) (parsedInbound, error) {
	return parsedInbound{Intents: parseSlackInbound(raw), Recipient: slackRecipient(raw), Sender: slackSender(raw)}, nil
}

func isChatID(v any) bool {
	switch v.(type) {
	case float64, string:
		return true
	}
	return false
}

func firstObj(values ...any) *jsObj {
	for _, v := range values {
		if o := objOrNil(v); o != nil {
			return o
		}
	}
	return nil
}

// telegramRecipient is recipientFromTelegramUpdate().
func telegramRecipient(raw any) *jsObj {
	u := objOrNil(raw)
	if u == nil {
		return nil
	}
	message := firstObj(jsGet(u, "message"), jsGet(u, "edited_message"), jsGet(u, "channel_post"), jsGet(jsGet(u, "callback_query"), "message"))
	chatID := jsGet(jsGet(message, "chat"), "id")
	if !isChatID(chatID) {
		return nil
	}
	return jsObjOf("kind", "telegram", "chatId", chatID)
}

// telegramSender is senderFromTelegramUpdate().
func telegramSender(raw any) *jsObj {
	u := objOrNil(raw)
	if u == nil {
		return nil
	}
	from := firstObj(jsGet(jsGet(u, "callback_query"), "from"), jsGet(jsGet(u, "message"), "from"), jsGet(jsGet(u, "edited_message"), "from"))
	id := jsGet(from, "id")
	if !isChatID(id) {
		return nil
	}
	var names []string
	for _, k := range []string{"first_name", "last_name"} {
		if s, _ := jsGet(from, k).(string); s != "" {
			names = append(names, s)
		}
	}
	name := strings.Join(names, " ")
	if name == "" {
		name, _ = jsGet(from, "username").(string)
	}
	out := jsObjOf("id", jsStringOf(id))
	setIfText(out, "displayName", name)
	return out
}

// intentFromButtonID is intentFromButtonId() in packages/channels/src/inbound/button-id.ts.
func intentFromButtonID(id string) *jsObj {
	verb, rest, ok := strings.Cut(id, ":")
	if !ok {
		return nil
	}
	switch verb {
	case "approve", "reject":
		return jsObjOf("kind", "decision", "decisionRef", rest, "choice", verb)
	case "select":
		ref, option, ok := strings.Cut(rest, ":")
		if !ok {
			return nil
		}
		return jsObjOf("kind", "select", "ref", ref, "optionId", option)
	}
	return nil
}

func telegramIntent(u *jsObj) *jsObj {
	if cq := objOrNil(jsGet(u, "callback_query")); cq != nil {
		if data, _ := jsGet(cq, "data").(string); data != "" {
			return intentFromButtonID(data)
		}
		return nil
	}
	message := firstObj(jsGet(u, "message"), jsGet(u, "edited_message"), jsGet(u, "channel_post"))
	if message == nil {
		return nil
	}
	text, _ := jsGet(message, "text").(string)
	if text == "" {
		return nil
	}
	threadRef := ""
	if chatID := jsGet(jsGet(message, "chat"), "id"); isChatID(chatID) {
		threadRef = jsStringOf(chatID)
	}
	if strings.HasPrefix(text, "/") {
		segs := splitJSSpace(text[1:])
		if segs[0] != "" {
			it := jsObjOf("kind", "invoke", "command", segs[0])
			if rest := strings.Join(segs[1:], " "); rest != "" {
				it.set("args", jsObjOf("text", rest))
			}
			return it
		}
	}
	it := jsObjOf("kind", "utterance", "text", text)
	setIfText(it, "threadRef", threadRef)
	return it
}

// parseTelegramInbound is parseTelegramInbound(): one update, a getUpdates
// envelope, or an array of updates.
func parseTelegramInbound(raw any) []*jsObj {
	var updates []any
	switch t := raw.(type) {
	case []any:
		updates = t
	case *jsObj:
		if list, ok := jsGet(t, "result").([]any); ok {
			updates = list
		} else {
			updates = []any{t}
		}
	case nil:
	default:
		updates = []any{t}
	}
	var intents []*jsObj
	for _, u := range updates {
		if o := objOrNil(u); o != nil {
			if it := telegramIntent(o); it != nil {
				intents = append(intents, it)
			}
		}
	}
	return intents
}

func parseTelegramUpdate(raw any, _ map[string]string) (parsedInbound, error) {
	return parsedInbound{Intents: parseTelegramInbound(raw), Recipient: telegramRecipient(raw), Sender: telegramSender(raw)}, nil
}

// parseHUDUpdate is parseHudUpdate() in packages/channels/src/hud/channel.ts.
func parseHUDUpdate(raw any, env map[string]string) (parsedInbound, error) {
	body := objOrNil(raw)
	text := func(k string) string { s, _ := jsGet(body, k).(string); return s }
	pack := hudWorkspace(env)
	recipient := jsObjOf("kind", "hud", "workspace", pack)
	id := text("operatorId")
	if id == "" {
		id = pack + "-operator"
	}
	sender := jsObjOf("id", id)
	setIfText(sender, "displayName", text("operatorName"))
	sender.set("workspaceId", pack)
	switch kind := text("kind"); kind {
	case "decision":
		ref, choice := text("decisionRef"), text("choice")
		if ref == "" {
			return parsedInbound{}, errors.New("decisionRef is required")
		}
		if choice != "approve" && choice != "reject" {
			return parsedInbound{}, errors.New("choice must be approve or reject")
		}
		it := jsObjOf("kind", "decision", "decisionRef", ref, "choice", choice)
		setIfText(it, "reason", text("reason"))
		return parsedInbound{Intents: []*jsObj{it}, Recipient: recipient, Sender: sender}, nil
	case "utterance":
		t := jsTrim(text("text"))
		if t == "" {
			return parsedInbound{}, errors.New("text is required")
		}
		return parsedInbound{Intents: []*jsObj{jsObjOf("kind", "utterance", "text", t)}, Recipient: recipient, Sender: sender}, nil
	default:
		if kind == "" {
			kind = "missing"
		}
		return parsedInbound{}, errors.New("unsupported inbound event kind: " + kind)
	}
}

const slackMaxSkew = 5 * 60

var slackNow = func() int64 { return time.Now().Unix() }

// verifySlackRequest is verifySlackRequest(): the v0 HMAC of the timestamp
// and body, within five minutes. It fails closed with no signing secret.
func verifySlackRequest(h http.Header, body []byte, env map[string]string) bool {
	secret := env["SLACK_SIGNING_SECRET"]
	timestamp, signature := h.Get("X-Slack-Request-Timestamp"), h.Get("X-Slack-Signature")
	if secret == "" || timestamp == "" || signature == "" {
		return false
	}
	ts, err := strconv.ParseFloat(jsTrim(timestamp), 64)
	if err != nil || math.IsInf(ts, 0) || math.Abs(float64(slackNow())-ts) > slackMaxSkew {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + timestamp + ":"))
	mac.Write(body)
	return hmac.Equal([]byte("v0="+hex.EncodeToString(mac.Sum(nil))), []byte(signature))
}

// verifyTelegramRequest matches the secret_token header in constant time.
func verifyTelegramRequest(h http.Header, _ []byte, env map[string]string) bool {
	secret, header := env["TELEGRAM_WEBHOOK_SECRET"], h.Get("X-Telegram-Bot-Api-Secret-Token")
	return secret != "" && header != "" && subtle.ConstantTimeCompare([]byte(header), []byte(secret)) == 1
}

var hexSignature = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

// verifyHUDRequest is verifyHudRequest(): a hex HMAC of the body.
func verifyHUDRequest(h http.Header, body []byte, env map[string]string) bool {
	secret := jsTrim(env["HUD_CHANNEL_SECRET"])
	supplied := strings.TrimPrefix(h.Get("X-Openneko-Hud-Signature"), "sha256=")
	if secret == "" || !hexSignature.MatchString(supplied) {
		return false
	}
	got, _ := hex.DecodeString(supplied)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}
