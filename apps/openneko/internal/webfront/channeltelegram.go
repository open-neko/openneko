package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

var telegramAPIBase = "https://api.telegram.org"

var telegramHTMLEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func telegramEscape(s string) string { return telegramHTMLEscaper.Replace(s) }

const stashOpen, stashClose = "", ""

const sp = `[` + jsSpaceClass + `]`

var (
	mdCode     = regexp.MustCompile("`([^`]+)`")
	mdLink     = regexp.MustCompile(`\[([^\]]+)\]\(([^)\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]+)\)`)
	mdBold     = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	mdBoldU    = regexp.MustCompile(`__([^_\n]+)__`)
	mdItalic   = regexp.MustCompile(`(^|[^*])\*([^*\n]+)\*`)
	mdStrike   = regexp.MustCompile(`~~([^~\n]+)~~`)
	mdStash    = regexp.MustCompile(stashOpen + `(\d+)` + stashClose)
	mdFence    = regexp.MustCompile("^```(\\w*)" + sp + "*$")
	mdFenceEnd = regexp.MustCompile("^```" + sp + "*$")
	mdQuote    = regexp.MustCompile(`^>` + sp + `?(.*)$`)
	mdHeading  = regexp.MustCompile(`^#{1,6}` + sp + `+(.*)$`)
	mdListItem = regexp.MustCompile(`^` + sp + `*(?:[-*+]|\d+[.)])` + sp + `+(.*)$`)
	mdBlankRun = regexp.MustCompile(`\n{3,}`)
)

func replaceGroups(re *regexp.Regexp, s string, fn func(groups []string) string) string {
	var b strings.Builder
	last := 0
	for _, loc := range re.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(s[last:loc[0]])
		groups := make([]string, len(loc)/2)
		for i := range groups {
			if loc[2*i] >= 0 {
				groups[i] = s[loc[2*i]:loc[2*i+1]]
			}
		}
		b.WriteString(fn(groups))
		last = loc[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// inlineMd is inlineMd() in packages/channels/src/telegram/markdown.ts.
func inlineMd(text string) string {
	var stash []string
	keep := func(fragment string) string {
		stash = append(stash, fragment)
		return stashOpen + strconv.Itoa(len(stash)-1) + stashClose
	}
	s := replaceGroups(mdCode, text, func(g []string) string { return keep("<code>" + telegramEscape(g[1]) + "</code>") })
	s = replaceGroups(mdLink, s, func(g []string) string {
		return keep(`<a href="` + telegramEscape(g[2]) + `">` + telegramEscape(g[1]) + "</a>")
	})
	s = telegramEscape(s)
	s = mdBold.ReplaceAllString(s, "<b>${1}</b>")
	s = mdBoldU.ReplaceAllString(s, "<b>${1}</b>")
	s = mdItalic.ReplaceAllString(s, "${1}<i>${2}</i>")
	s = mdStrike.ReplaceAllString(s, "<s>${1}</s>")
	return replaceGroups(mdStash, s, func(g []string) string {
		n, _ := strconv.Atoi(g[1])
		if n < len(stash) {
			return stash[n]
		}
		return ""
	})
}

func isMdRule(line string) bool {
	t := jsTrim(line)
	if len(t) < 3 || !strings.ContainsRune("-*_", rune(t[0])) {
		return false
	}
	return strings.Count(t, t[:1]) == len(t)
}

// mdToTelegramHTML is mdToTelegramHtml() in packages/channels/src/telegram/markdown.ts.
func mdToTelegramHTML(md string) string {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var out []string
	var quote []string
	flush := func() {
		if len(quote) > 0 {
			out = append(out, "<blockquote>"+strings.Join(quote, "\n")+"</blockquote>")
		}
		quote = nil
	}
	for i := 0; i < len(lines); {
		line := lines[i]
		if fence := mdFence.FindStringSubmatch(line); fence != nil {
			flush()
			var code []string
			i++
			for i < len(lines) && !mdFenceEnd.MatchString(lines[i]) {
				code = append(code, lines[i])
				i++
			}
			i++
			body := telegramEscape(strings.Join(code, "\n"))
			if fence[1] != "" {
				out = append(out, `<pre><code class="language-`+fence[1]+`">`+body+"</code></pre>")
			} else {
				out = append(out, "<pre>"+body+"</pre>")
			}
			continue
		}
		if bq := mdQuote.FindStringSubmatch(line); bq != nil {
			quote = append(quote, inlineMd(bq[1]))
			i++
			continue
		}
		flush()
		switch {
		case mdHeading.MatchString(line):
			out = append(out, "<b>"+inlineMd(mdHeading.FindStringSubmatch(line)[1])+"</b>")
		case isMdRule(line):
			out = append(out, "—")
		case mdListItem.MatchString(line):
			out = append(out, "• "+inlineMd(mdListItem.FindStringSubmatch(line)[1]))
		case jsTrim(line) == "":
			out = append(out, "")
		default:
			out = append(out, inlineMd(line))
		}
		i++
	}
	flush()
	return jsTrim(mdBlankRun.ReplaceAllString(strings.Join(out, "\n"), "\n\n"))
}

// inlineBlock is inlineBlock() in packages/channels/src/telegram/markdown.ts.
func inlineBlock(md string) string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(l, "```") {
			continue
		}
		switch {
		case mdHeading.MatchString(l):
			out = append(out, "<b>"+inlineMd(mdHeading.FindStringSubmatch(l)[1])+"</b>")
		case mdListItem.MatchString(l):
			out = append(out, "• "+inlineMd(mdListItem.FindStringSubmatch(l)[1]))
		case mdQuote.MatchString(l):
			out = append(out, inlineMd(mdQuote.FindStringSubmatch(l)[1]))
		default:
			out = append(out, inlineMd(l))
		}
	}
	return jsTrim(mdBlankRun.ReplaceAllString(strings.Join(out, "\n"), "\n\n"))
}

var moodIcons = map[string]string{"good": "✅", "watch": "👀", "act": "🚨"}

func joinNonEmpty(parts []string, sep string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// renderSurface is renderSurface() in packages/channels/src/telegram/surface.ts.
func renderSurface(surfaces []any) (string, []string) {
	comps := map[string]*jsObj{}
	var order []string
	var add func(comp *jsObj)
	add = func(comp *jsObj) {
		if raw, ok := comp.vals["children"].([]any); ok {
			ids := []any{}
			for _, child := range raw {
				switch c := child.(type) {
				case string:
					ids = append(ids, c)
				case *jsObj:
					if _, has := c.vals["id"]; has {
						ids = append(ids, c.vals["id"])
						add(c)
					}
				}
			}
			copied := jsSpread(comp)
			copied.set("children", ids)
			comp = copied
		}
		if id := str(comp.vals["id"]); jsTruthy(comp.vals["id"]) {
			if _, seen := comps[id]; !seen {
				order = append(order, id)
			}
			comps[id] = comp
		}
	}
	for _, m := range surfaces {
		if list, ok := jsGet(jsGet(m, "updateComponents"), "components").([]any); ok {
			for _, c := range list {
				if obj, ok := c.(*jsObj); ok {
					add(obj)
				}
			}
		}
	}
	var followups []string
	childIDs := func(comp *jsObj) []string {
		var ids []string
		for _, c := range jsArray(comp.vals["children"]) {
			ids = append(ids, str(c))
		}
		if len(ids) > 0 || str(comp.vals["id"]) != "root" {
			return ids
		}
		for _, id := range order {
			if id != "root" {
				ids = append(ids, id)
			}
		}
		return ids
	}
	var renderOne func(id string) string
	renderMany := func(ids []string) string {
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = renderOne(id)
		}
		return joinNonEmpty(parts, "\n\n")
	}
	field := func(c *jsObj, k string) string { return str(c.vals[k]) }
	renderOne = func(id string) string {
		c := comps[id]
		if c == nil {
			return ""
		}
		switch field(c, "component") {
		case "Answer", "Briefing":
			title := field(c, "title")
			if title == "" {
				title = field(c, "greeting")
			}
			var head []string
			if e := field(c, "eyebrow"); e != "" {
				head = append(head, "<i>"+telegramEscape(e)+"</i>")
			}
			if title != "" {
				head = append(head, "<b>"+telegramEscape(title)+"</b>")
			}
			if s := field(c, "subtitle"); s != "" {
				head = append(head, telegramEscape(s))
			}
			return joinNonEmpty([]string{strings.Join(head, "\n"), renderMany(childIDs(c))}, "\n\n")
		case "Markdown":
			return mdToTelegramHTML(field(c, "text"))
		case "MetricCard", "BriefingCard":
			head := "<b>" + telegramEscape(field(c, "metric")) + "</b>"
			if icon := moodIcons[field(c, "mood")]; icon != "" {
				head = icon + " " + head
			}
			if l := field(c, "label"); l != "" {
				head += " — " + telegramEscape(l)
			}
			if d := field(c, "detail"); d != "" {
				return head + "\n" + inlineBlock(d)
			}
			return head
		case "Table":
			return renderTelegramTable(c)
		case "Callout":
			head := ""
			if icon := moodIcons[field(c, "mood")]; icon != "" {
				head = icon + " "
			}
			if t := field(c, "title"); t != "" {
				head += "<b>" + telegramEscape(t) + "</b>"
			}
			return "<blockquote>" + joinNonEmpty([]string{jsTrim(head), inlineBlock(field(c, "text"))}, "\n") + "</blockquote>"
		case "Section":
			title := ""
			if t := field(c, "title"); t != "" {
				title = "<b>" + telegramEscape(t) + "</b>"
			}
			return joinNonEmpty([]string{title, renderMany(childIDs(c))}, "\n")
		case "Confirmation":
			label := ""
			if l := field(c, "label"); l != "" {
				label = "<b>" + telegramEscape(l) + "</b>"
			}
			title := ""
			if t := field(c, "title"); t != "" {
				title = telegramEscape(t)
			}
			return joinNonEmpty([]string{jsTrim("✅ " + label), title, renderMany(childIDs(c))}, "\n")
		case "Choice":
			for _, o := range jsArray(c.vals["options"]) {
				label := str(jsGet(o, "label"))
				if label == "" {
					label = str(jsGet(o, "prompt"))
				}
				if label != "" {
					followups = append(followups, label)
				}
			}
			return ""
		case "Divider":
			if l := field(c, "label"); l != "" {
				return "— " + telegramEscape(l) + " —"
			}
			return "———"
		}
		return ""
	}
	if comps["root"] != nil {
		return renderOne("root"), followups
	}
	return renderMany(order), followups
}

func renderTelegramTable(c *jsObj) string {
	columns := jsArray(c.vals["columns"])
	if len(columns) == 0 {
		return ""
	}
	rows := jsArray(c.vals["rows"])
	headers := make([]string, len(columns))
	widths := make([]int, len(columns))
	for i, col := range columns {
		headers[i] = str(jsGet(col, "label"))
		widths[i] = max(utf16Len(headers[i]), 1)
	}
	cells := make([][]string, len(rows))
	for r, row := range rows {
		cells[r] = make([]string, len(columns))
		for i, col := range columns {
			cells[r][i] = str(jsGet(row, str(jsGet(col, "key"))))
			widths[i] = max(widths[i], utf16Len(cells[r][i]))
		}
	}
	format := func(row []string) string {
		out := make([]string, len(row))
		for i, cell := range row {
			pad := strings.Repeat(" ", max(0, widths[i]-utf16Len(cell)))
			if str(jsGet(columns[i], "align")) == "right" {
				out[i] = pad + cell
			} else {
				out[i] = cell + pad
			}
		}
		return strings.Join(out, "  ")
	}
	rules := make([]string, len(widths))
	for i, w := range widths {
		rules[i] = strings.Repeat("─", w)
	}
	grid := []string{format(headers), strings.Join(rules, "  ")}
	for _, row := range cells {
		grid = append(grid, format(row))
	}
	caption := ""
	if cap := str(c.vals["caption"]); cap != "" {
		caption = telegramEscape(cap) + "\n"
	}
	return caption + "<pre>" + telegramEscape(strings.Join(grid, "\n")) + "</pre>"
}

var (
	paragraphBreak = regexp.MustCompile(`\n\n+`)
	htmlTag        = regexp.MustCompile(`(?i)<(/?)([a-z0-9-]+)(?:` + sp + `[^>]*)?>`)
)

func telegramSummarizeBody(body, fidelity string) string {
	if fidelity == "full" {
		return body
	}
	first := paragraphBreak.Split(body, 2)[0]
	if fidelity == "summary" {
		return first
	}
	runes := []rune(first)
	for i := 1; i < len(runes); i++ {
		if strings.ContainsRune(".!?", runes[i-1]) && jsSpace(runes[i]) {
			return string(runes[:i])
		}
	}
	return first
}

func openTags(html string) []string {
	var open []string
	for _, m := range htmlTag.FindAllStringSubmatch(html, -1) {
		name := strings.ToLower(m[2])
		if m[1] == "" {
			open = append(open, name)
			continue
		}
		for i := len(open) - 1; i >= 0; i-- {
			if open[i] == name {
				open = append(open[:i], open[i+1:]...)
				break
			}
		}
	}
	return open
}

func lastIndexUnits(units []uint16, sep string) int {
	s := utf16.Encode([]rune(sep))
	for i := len(units) - len(s); i >= 0; i-- {
		match := true
		for j := range s {
			if units[i+j] != s[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// splitHTML is splitHtml() in packages/channels/src/telegram/projection.ts;
// lengths count UTF-16 units, as JavaScript does.
func splitHTML(html string, limit int) []string {
	if limit <= 0 || utf16Len(html) <= limit {
		return []string{html}
	}
	var out []string
	prefix := ""
	rest := utf16.Encode([]rune(html))
	for utf16Len(prefix)+len(rest) > limit {
		room := max(1, limit-utf16Len(prefix)-24)
		at := min(room, len(rest))
		window := rest[:at]
		for _, sep := range []string{"\n\n", "\n", " "} {
			if idx := lastIndexUnits(window, sep); float64(idx) > float64(room)*0.4 {
				at = idx + len(sep)
				break
			}
		}
		head := rest[:at]
		if lt := lastIndexUnits(head, "<"); lt > lastIndexUnits(head, ">") && lt > 0 {
			at = lt
		}
		chunk := prefix + string(utf16.Decode(rest[:at]))
		open := openTags(chunk)
		closers := ""
		for i := len(open) - 1; i >= 0; i-- {
			closers += "</" + open[i] + ">"
		}
		out = append(out, jsTrimEnd(chunk+closers))
		prefix = ""
		for _, t := range open {
			prefix += "<" + t + ">"
		}
		rest = rest[at:]
		for len(rest) > 0 && rest[0] == '\n' {
			rest = rest[1:]
		}
	}
	if tail := jsTrimEnd(prefix + string(utf16.Decode(rest))); tail != "" {
		out = append(out, tail)
	}
	return out
}

var httpURL = regexp.MustCompile(`^https?://`)

// projectTelegram is projectTelegram() in packages/channels: the events as
// sendMessage payloads.
func projectTelegram(events []any, profile channelProfile) []*jsObj {
	var parts, followups []string
	var keyboard []any
	for _, event := range events {
		switch jsGet(event, "kind") {
		case "converse":
			if surfaces := jsArray(jsGet(jsGet(event, "enrichment"), "surfaces")); len(surfaces) > 0 {
				html, more := renderSurface(surfaces)
				if html != "" {
					parts = append(parts, html)
				}
				followups = append(followups, more...)
			} else {
				parts = append(parts, mdToTelegramHTML(str(jsGet(event, "text"))))
			}
		case "inform":
			if surfaces := jsArray(jsGet(jsGet(event, "enrichment"), "surfaces")); len(surfaces) > 0 {
				html, more := renderSurface(surfaces)
				if html != "" {
					parts = append(parts, html)
				}
				followups = append(followups, more...)
				continue
			}
			head := "<b>" + telegramEscape(str(jsGet(event, "title"))) + "</b>"
			if icon := moodIcons[str(jsGet(event, "mood"))]; icon != "" {
				head = icon + " " + head
			}
			if body := telegramSummarizeBody(str(jsGet(event, "body")), profile.Fidelity); body != "" {
				head += "\n" + mdToTelegramHTML(body)
			}
			if metric := jsGet(event, "metric"); jsTruthy(metric) {
				head += "\n<b>" + telegramEscape(str(jsGet(metric, "label"))) + ":</b> " + telegramEscape(str(jsGet(metric, "value")))
			}
			parts = append(parts, head)
		case "ask":
			parts = append(parts, mdToTelegramHTML(str(jsGet(event, "prompt"))))
			if keyboard == nil && profile.QuickReplies {
				if buttons := telegramAskButtons(event); len(buttons) > 0 {
					keyboard = buttons
				}
			}
		case "resolve":
			mark := "⚠️"
			switch jsGet(event, "status") {
			case "succeeded":
				mark = "✅"
			case "rejected":
				mark = "🚫"
			}
			parts = append(parts, mark+" "+telegramEscape(str(jsGet(event, "summary"))))
		case "highlight":
			lines := []string{"<b>Key figures</b>"}
			for _, m := range jsArray(jsGet(event, "metrics")) {
				line := "• <b>" + telegramEscape(str(jsGet(m, "value"))) + "</b> — " + telegramEscape(str(jsGet(m, "label")))
				if sub := str(jsGet(m, "sub")); sub != "" {
					line += " <i>(" + telegramEscape(sub) + ")</i>"
				}
				lines = append(lines, line)
			}
			parts = append(parts, strings.Join(lines, "\n"))
		case "offer":
			ref, label := str(jsGet(event, "artifactRef")), telegramEscape(str(jsGet(event, "label")))
			if httpURL.MatchString(ref) {
				parts = append(parts, `📎 <a href="`+telegramEscape(ref)+`">`+label+"</a>")
			} else {
				parts = append(parts, "📎 "+label)
			}
		}
	}
	var replyKeyboard []any
	if len(followups) > 0 {
		labels := followups[:min(6, len(followups))]
		if keyboard == nil && profile.QuickReplies && joinNonEmpty(parts, "") != "" {
			for _, f := range labels {
				replyKeyboard = append(replyKeyboard, []any{jsObjOf("text", f)})
			}
		} else {
			list := make([]string, len(labels))
			for i, f := range labels {
				list[i] = "• " + telegramEscape(f)
			}
			parts = append(parts, "💡 <b>Ask next</b>\n"+strings.Join(list, "\n"))
		}
	}
	full := joinNonEmpty(parts, "\n\n")
	if full == "" {
		return nil
	}
	var messages []*jsObj
	for _, text := range splitHTML(full, profile.MaxOutboundChars) {
		messages = append(messages, jsObjOf("text", text, "parse_mode", "HTML"))
	}
	last := messages[len(messages)-1]
	if keyboard != nil {
		last.set("reply_markup", jsObjOf("inline_keyboard", keyboard))
	} else if replyKeyboard != nil {
		last.set("reply_markup", jsObjOf("keyboard", replyKeyboard, "one_time_keyboard", true, "resize_keyboard", true,
			"input_field_placeholder", "Ask a follow-up…"))
	}
	return messages
}

func telegramAskButtons(event any) []any {
	ref := str(jsGet(event, "decisionRef"))
	options := jsArray(jsGet(event, "options"))
	if jsGet(event, "ask") == "choice" && len(options) > 0 {
		var rows []any
		for _, o := range options[:min(5, len(options))] {
			rows = append(rows, []any{jsObjOf("text", jsSlice(str(jsGet(o, "label")), 64),
				"callback_data", "select:"+ref+":"+str(jsGet(o, "id")))})
		}
		return rows
	}
	if jsGet(event, "ask") == "approval" {
		return []any{[]any{
			jsObjOf("text", "✅ Approve", "callback_data", "approve:"+ref),
			jsObjOf("text", "🚫 Reject", "callback_data", "reject:"+ref),
		}}
	}
	return nil
}

// deliverTelegram is deliverTelegram() in packages/channels/src/telegram/channel.ts.
func deliverTelegram(ctx context.Context, recipient any, events []any, env map[string]string) (channelDeliverResult, error) {
	messages := projectTelegram(events, telegramProfile)
	if len(messages) == 0 {
		return channelDeliverResult{}, nil
	}
	chatID := jsGet(recipient, "chatId")
	switch chatID.(type) {
	case string, float64:
	default:
		return channelDeliverResult{}, errors.New("recipient.chatId is required (the Telegram chat or user id to deliver to)")
	}
	token := env["TELEGRAM_BOT_TOKEN"]
	if token == "" {
		log.Printf("[channel-telegram dry-run] chat=%s", str(chatID))
		return channelDeliverResult{Ref: fmt.Sprintf("dry-run:%d", len(messages))}, nil
	}
	lastID := ""
	for _, message := range messages {
		body := jsObjOf("chat_id", chatID)
		for _, k := range message.ordered() {
			body.set(k, message.vals[k])
		}
		raw, status, err := postChannelJSON(ctx, telegramAPIBase+"/bot"+token+"/sendMessage", nil, body)
		if err != nil {
			return channelDeliverResult{}, fmt.Errorf("Telegram sendMessage network error: %s", strings.ReplaceAll(err.Error(), token, "<token>"))
		}
		var envelope struct {
			OK          bool   `json:"ok"`
			Description string `json:"description"`
			Result      struct {
				MessageID *float64 `json:"message_id"`
			} `json:"result"`
		}
		if len(raw) > 0 && json.Unmarshal(raw, &envelope) != nil {
			return channelDeliverResult{}, fmt.Errorf("Telegram sendMessage returned non-JSON (HTTP %d): %s", status, jsSlice(string(raw), 200))
		}
		if !envelope.OK {
			desc := envelope.Description
			if desc == "" {
				desc = "no description"
			}
			return channelDeliverResult{}, fmt.Errorf("Telegram sendMessage returned ok=false (%s)", desc)
		}
		if envelope.Result.MessageID != nil {
			lastID = jsNumberText(*envelope.Result.MessageID)
		}
	}
	return channelDeliverResult{Delivered: true, Ref: lastID}, nil
}
