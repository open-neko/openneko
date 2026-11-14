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
	"net/url"
	"regexp"
	"strings"
	"time"
)

var slackAPIBase = "https://slack.com/api"

const channelSendTimeout = 20 * time.Second

func slackSection(markdown string, fields []any) *jsObj {
	block := jsObjOf("type", "section", "text", jsObjOf("type", "mrkdwn", "text", markdown))
	if fields != nil {
		block.set("fields", fields)
	}
	return block
}

func slackContext(text string) *jsObj {
	return jsObjOf("type", "context", "elements", []any{jsObjOf("type", "mrkdwn", "text", text)})
}

func slackButton(actionID, style, value, label string) *jsObj {
	button := jsObjOf("type", "button", "action_id", actionID)
	if style != "" {
		button.set("style", style)
	}
	button.set("value", value)
	button.set("text", jsObjOf("type", "plain_text", "text", label))
	return button
}

var firstSentencePattern = regexp.MustCompile(`^[^\n\r\x{2028}\x{2029}]*?[.!?](?:[` + jsSpaceClass + `]|$)`)

// summarizeBody is summarizeBody() in packages/channels/src/projections/degrade.ts.
func summarizeBody(body, fidelity string) string {
	switch fidelity {
	case "full":
		return body
	case "headline":
		return ""
	}
	if m := firstSentencePattern.FindString(body); m != "" {
		return jsTrim(m)
	}
	return jsTrim(body)
}

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	}
	return jsStringOf(v)
}

// slackProjection is slackProjection() in packages/channels.
func slackProjection(events []any, profile channelProfile) ([]any, string) {
	blocks := []any{}
	var lines []string
	for _, event := range events {
		switch jsGet(event, "kind") {
		case "converse":
			text := str(jsGet(event, "text"))
			blocks = append(blocks, slackSection(text, nil))
			lines = append(lines, text)
		case "inform":
			title := str(jsGet(event, "title"))
			body := summarizeBody(str(jsGet(event, "body")), profile.Fidelity)
			var fields []any
			if metric := jsGet(event, "metric"); metric != nil {
				fields = []any{jsObjOf("type", "mrkdwn", "text", "*"+str(jsGet(metric, "label"))+"*\n"+str(jsGet(metric, "value")))}
			}
			text := "*" + title + "*"
			if body != "" {
				text += "\n" + body
			}
			blocks = append(blocks, slackSection(text, fields))
			if jsTruthy(jsGet(event, "series")) && !profile.Charts {
				blocks = append(blocks, slackContext("📊 Chart available in the web dashboard."))
			}
			lines = append(lines, title)
		case "ask":
			prompt := str(jsGet(event, "prompt"))
			blocks = append(blocks, slackSection(prompt, nil))
			ref := str(jsGet(event, "decisionRef"))
			options := jsArray(jsGet(event, "options"))
			switch {
			case !profile.CanApproveInline:
				blocks = append(blocks, slackContext("Open the web dashboard to respond."))
			case jsGet(event, "ask") == "choice" && len(options) > 0:
				var elements []any
				for _, o := range options {
					id := str(jsGet(o, "id"))
					elements = append(elements, slackButton("select:"+id, "", ref+":"+id, str(jsGet(o, "label"))))
				}
				blocks = append(blocks, jsObjOf("type", "actions", "elements", elements))
			case jsGet(event, "ask") == "approval":
				blocks = append(blocks, jsObjOf("type", "actions", "elements", []any{
					slackButton("approve", "primary", ref, "Approve"), slackButton("reject", "danger", ref, "Reject")}))
			}
			lines = append(lines, prompt)
		case "resolve":
			mark := "✗"
			switch jsGet(event, "status") {
			case "succeeded":
				mark = "✓"
			case "rejected":
				mark = "⊘"
			}
			blocks = append(blocks, slackContext(mark+" "+str(jsGet(event, "summary"))))
		case "offer":
			blocks = append(blocks, slackSection("📎 "+str(jsGet(event, "label")), nil))
		case "highlight":
			var fields []any
			var parts []string
			for _, m := range jsArray(jsGet(event, "metrics")) {
				text := "*" + str(jsGet(m, "label")) + "*\n" + str(jsGet(m, "value"))
				if sub := str(jsGet(m, "sub")); sub != "" {
					text += " _(" + sub + ")_"
				}
				fields = append(fields, jsObjOf("type", "mrkdwn", "text", text))
				parts = append(parts, str(jsGet(m, "label"))+" "+str(jsGet(m, "value")))
			}
			if fields == nil {
				fields = []any{}
			}
			blocks = append(blocks, slackSection("*Key figures*", fields))
			lines = append(lines, strings.Join(parts, " · "))
		}
	}
	return blocks, strings.Join(lines, " · ")
}

// deliverSlack is deliverSlack() in packages/channels/src/slack/channel.ts.
func deliverSlack(ctx context.Context, recipient any, events []any, env map[string]string) (channelDeliverResult, error) {
	blocks, text := slackProjection(events, slackProfile)
	if len(blocks) == 0 {
		return channelDeliverResult{}, nil
	}
	channel, ok := jsGet(recipient, "channel").(string)
	if !ok || channel == "" {
		return channelDeliverResult{}, errors.New("recipient.channel is required (the Slack channel or DM id to deliver to)")
	}
	threadTS, _ := jsGet(recipient, "thread_ts").(string)
	user, _ := jsGet(recipient, "user").(string)
	ephemeral := jsGet(recipient, "ephemeral") == true && user != ""
	token := env["SLACK_BOT_TOKEN"]
	if token == "" {
		log.Printf("[channel-slack dry-run] channel=%s", channel)
		return channelDeliverResult{Ref: fmt.Sprintf("dry-run:%d", len(blocks))}, nil
	}
	body := jsObjOf("channel", channel, "blocks", blocks, "text", text)
	if threadTS != "" {
		body.set("thread_ts", threadTS)
	}
	method := "chat.postMessage"
	if ephemeral {
		method = "chat.postEphemeral"
		body.set("user", user)
	}
	var envelope struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		TS    any    `json:"ts"`
	}
	raw, status, err := postChannelJSON(ctx, slackAPIBase+"/"+url.PathEscape(method), map[string]string{"Authorization": "Bearer " + token}, body)
	if err != nil {
		return channelDeliverResult{}, fmt.Errorf("Slack %s network error: %w", method, err)
	}
	if len(raw) == 0 {
		return channelDeliverResult{}, fmt.Errorf("Slack %s returned ok=false (no error code)", method)
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return channelDeliverResult{}, fmt.Errorf("Slack %s returned non-JSON (HTTP %d): %s", method, status, jsSlice(string(raw), 200))
	}
	if !envelope.OK {
		code := envelope.Error
		if code == "" {
			code = "no error code"
		}
		return channelDeliverResult{}, fmt.Errorf("Slack %s returned ok=false (%s)", method, code)
	}
	ts, _ := envelope.TS.(string)
	return channelDeliverResult{Delivered: true, Ref: ts}, nil
}

func postChannelJSON(ctx context.Context, target string, headers map[string]string, body any) ([]byte, int, error) {
	return postChannelBody(ctx, target, headers, []byte(jsStringify(body, false)), channelSendTimeout)
}

func postChannelBody(ctx context.Context, target string, headers map[string]string, body []byte, timeout time.Duration) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, 0, fmt.Errorf("timed out after %dms", timeout.Milliseconds())
		}
		return nil, 0, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	return raw, res.StatusCode, nil
}
