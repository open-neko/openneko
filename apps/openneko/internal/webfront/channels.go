package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

// channelProfile is CapabilityProfile in packages/interaction, with the
// fields the projections read.
type channelProfile struct {
	Fidelity         string
	Charts           bool
	CanApproveInline bool
	QuickReplies     bool
	MaxOutboundChars int
}

type channelSetting struct {
	Key          string
	Required     bool
	Autogenerate bool
}

type channelDeliverResult struct {
	Delivered bool
	Ref       string
}

type builtInChannel struct {
	ID       string
	Label    string
	Profile  channelProfile
	Settings []channelSetting
	Deliver  func(ctx context.Context, recipient any, events []any, env map[string]string) (channelDeliverResult, error)
}

var (
	slackProfile    = channelProfile{Fidelity: "summary", CanApproveInline: true, QuickReplies: true}
	telegramProfile = channelProfile{Fidelity: "summary", CanApproveInline: true, QuickReplies: true, MaxOutboundChars: 4096}
	hudProfile      = channelProfile{Fidelity: "full", Charts: true, CanApproveInline: true, QuickReplies: true}
)

// builtInChannels is BUILT_IN_CHANNELS in packages/channels, in its order.
var builtInChannels = []builtInChannel{
	{ID: "slack", Label: "Slack", Profile: slackProfile, Deliver: deliverSlack, Settings: []channelSetting{
		{Key: "SLACK_BOT_TOKEN", Required: true}, {Key: "SLACK_APP_TOKEN"}, {Key: "SLACK_SIGNING_SECRET"}}},
	{ID: "telegram", Label: "Telegram", Profile: telegramProfile, Deliver: deliverTelegram, Settings: []channelSetting{
		{Key: "TELEGRAM_BOT_TOKEN", Required: true}, {Key: "TELEGRAM_WEBHOOK_SECRET"}}},
	{ID: "hud", Label: "HUD surface", Profile: hudProfile, Deliver: deliverHUD, Settings: []channelSetting{
		{Key: "HUD_CHANNEL_SECRET", Required: true, Autogenerate: true}, {Key: "HUD_CHANNEL_WORKSPACE"}}},
}

// legacyChannelNames are the package names the channels had as plugins;
// old bindings and webhook URLs still carry them.
var legacyChannelNames = map[string]string{
	"@open-neko/plugin-slack":     "slack",
	"@open-neko/channel-telegram": "telegram",
	"@open-neko/channel-hud":      "hud",
}

func findBuiltInChannel(idOrLegacy string) *builtInChannel {
	id := idOrLegacy
	if mapped, ok := legacyChannelNames[idOrLegacy]; ok {
		id = mapped
	}
	for i := range builtInChannels {
		if builtInChannels[i].ID == id {
			return &builtInChannels[i]
		}
	}
	return nil
}

func channelEnabled(ch *builtInChannel, env map[string]string) bool {
	for _, s := range ch.Settings {
		if s.Required && jsTrim(env[s.Key]) == "" {
			return false
		}
	}
	return true
}

// enabledChannels is enabledChannelIds() of the worker's channel runtime.
func (a *API) enabledChannels() []*builtInChannel {
	store := secrets.ReadSoft(a.SecretsDir, func(msg string) { log.Printf("channels: %s", msg) })
	var out []*builtInChannel
	for i := range builtInChannels {
		if channelEnabled(&builtInChannels[i], store["channel."+builtInChannels[i].ID]) {
			out = append(out, &builtInChannels[i])
		}
	}
	return out
}

// deliverOnChannel is deliverOnChannel() of the worker's channel runtime.
func (a *API) deliverOnChannel(ctx context.Context, id string, recipient any, events []any) (channelDeliverResult, error) {
	ch := findBuiltInChannel(id)
	if ch == nil {
		return channelDeliverResult{}, fmt.Errorf("unknown channel %s", id)
	}
	store, err := secrets.Read(a.SecretsDir)
	if err != nil {
		return channelDeliverResult{}, err
	}
	env := store["channel."+ch.ID]
	if !channelEnabled(ch, env) {
		return channelDeliverResult{}, fmt.Errorf("channel %s is not turned on: set its required settings in Admin, Settings, Channels", ch.ID)
	}
	return ch.Deliver(ctx, recipient, events, env)
}

// ChannelDeliver is the channel_deliver job. A failed send or a send that
// delivers nothing fails the attempt, and River tries again.
func (a *API) ChannelDeliver(ctx context.Context, job jobs.ChannelDeliverArgs) error {
	recipient, err := jsParse(orEmptyObject(job.Recipient))
	if err != nil {
		return fmt.Errorf("channel-delivery: %s: recipient: %w", job.ChannelPlugin, err)
	}
	parsed, err := jsParse(orEmptyArray(job.Events))
	if err != nil {
		return fmt.Errorf("channel-delivery: %s: events: %w", job.ChannelPlugin, err)
	}
	result, err := a.deliverOnChannel(ctx, job.ChannelPlugin, recipient, jsArray(parsed))
	if err != nil {
		return fmt.Errorf("channel-delivery: %s failed: %w", job.ChannelPlugin, err)
	}
	if !result.Delivered {
		return fmt.Errorf("channel-delivery: %s reported delivered=false", job.ChannelPlugin)
	}
	ref := ""
	if result.Ref != "" {
		ref = " ref=" + result.Ref
	}
	log.Printf("[channel-delivery] %s delivered=true%s", job.ChannelPlugin, ref)
	return nil
}

func orEmptyObject(raw json.RawMessage) []byte {
	if len(raw) == 0 || string(raw) == "null" {
		return []byte("{}")
	}
	return raw
}

func orEmptyArray(raw json.RawMessage) []byte {
	if len(raw) == 0 || string(raw) == "null" {
		return []byte("[]")
	}
	return raw
}

var outputMoods = map[string]bool{"good": true, "watch": true, "act": true}

// deliverWorkflowOutput is the worker's output hook: the output goes as an
// inform event to each enabled binding whose audience holds the workflow.
func (a *API) deliverWorkflowOutput(ctx context.Context, orgID string, output *jsObj) error {
	rows, err := a.DB.Query(ctx, `select channel_plugin, audience, coalesce(recipient, '{}'::jsonb)::text
		from delivery_binding where org_id = $1 and enabled = true`, orgID)
	if err != nil {
		return err
	}
	type binding struct{ channel, audience, recipient string }
	var bindings []binding
	for rows.Next() {
		var b binding
		if err := rows.Scan(&b.channel, &b.audience, &b.recipient); err != nil {
			rows.Close()
			return err
		}
		bindings = append(bindings, b)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(bindings) == 0 {
		return nil
	}
	id, _ := jsGet(output, "id").(string)
	runID, _ := jsGet(output, "workflowRunId").(string)
	mood, _ := jsGet(output, "mood").(string)
	if !outputMoods[mood] {
		mood = "watch"
	}
	event := jsObjOf("kind", "inform", "id", id, "mood", mood, "title", jsGet(output, "title"), "body", jsGet(output, "body"))
	for _, b := range bindings {
		ok, err := a.audienceReceivesOutput(ctx, orgID, b.audience, runID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := a.queueChannelDelivery(ctx, orgID, b.channel, json.RawMessage(b.recipient), []any{event},
			"output-"+id+"-"+b.channel); err != nil {
			return err
		}
	}
	return nil
}

// audienceReceivesOutput is audienceReceivesOutput() of the worker: the
// audience ("*" or a group slug) must hold the workflow, and the watcher
// when a watcher fired the run. Everyone's grants count for every group.
func (a *API) audienceReceivesOutput(ctx context.Context, orgID, audience, runID string) (bool, error) {
	slug := audience
	if slug == "*" || slug == "" {
		slug = "everyone"
	}
	rows, err := a.DB.Query(ctx, `select id::text, slug from user_group where org_id = $1 and slug in ($2, 'everyone')`, orgID, slug)
	if err != nil {
		return false, err
	}
	type group struct{ id, slug string }
	var groups []group
	found := false
	for rows.Next() {
		var g group
		if err := rows.Scan(&g.id, &g.slug); err != nil {
			rows.Close()
			return false, err
		}
		found = found || g.slug == slug
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	var workflowID, kind string
	var payload []byte
	err = a.DB.QueryRow(ctx, `select workflow_id::text, trigger_kind, trigger_payload from workflow_run where org_id = $1 and id::text = $2`,
		orgID, runID).Scan(&workflowID, &kind, &payload)
	if errors.Is(err, pgx.ErrNoRows) || !found {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	holds := func(typ, id string) (bool, error) {
		for _, g := range groups {
			ok, err := a.groupHolds(ctx, orgID, g.id, g.slug, typ, id)
			if ok || err != nil {
				return ok, err
			}
		}
		return false, nil
	}
	ok, err := holds("workflow", workflowID)
	if !ok || err != nil {
		return false, err
	}
	if kind != "watcher" {
		return true, nil
	}
	var trigger struct {
		WatcherID any `json:"watcherId"`
	}
	_ = json.Unmarshal(payload, &trigger)
	watcherID := ""
	switch v := trigger.WatcherID.(type) {
	case nil:
	case string:
		watcherID = v
	default:
		raw, _ := json.Marshal(v)
		watcherID = strings.Trim(string(raw), `"`)
	}
	if watcherID == "" {
		return true, nil
	}
	return holds("watcher", watcherID)
}

// groupHolds is groupHolds() in packages/db/src/entitlements.ts.
func (a *API) groupHolds(ctx context.Context, orgID, groupID, slug, typ, itemID string) (bool, error) {
	if slug == "administrators" {
		return true, nil
	}
	rows, err := a.DB.Query(ctx, `select item_type, item_id from item_grant where org_id = $1 and group_id::text = $2`, orgID, groupID)
	if err != nil {
		return false, err
	}
	var packs []string
	for rows.Next() {
		var t, id string
		if err := rows.Scan(&t, &id); err != nil {
			rows.Close()
			return false, err
		}
		if t == typ && (id == itemID || id == "*") {
			rows.Close()
			return true, nil
		}
		if t == "pack" {
			packs = append(packs, id)
		}
	}
	if err := rows.Err(); err != nil || len(packs) == 0 {
		return false, err
	}
	containing, err := a.packsContaining(ctx, orgID, typ, itemID)
	if err != nil {
		return false, err
	}
	for _, p := range packs {
		if p == "*" && len(containing) > 0 {
			return true, nil
		}
		for _, c := range containing {
			if p == c {
				return true, nil
			}
		}
	}
	return false, nil
}
