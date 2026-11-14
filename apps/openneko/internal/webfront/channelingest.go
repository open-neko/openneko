package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

const (
	inboundMaxAttempts = 30
	inboundDedupTTL    = 7 * 24 * time.Hour
	maxInboundBody     = 256 << 10
)

var channelThreadNS = uuid.MustParse("8b9d2e7a-1c34-4f56-9a78-b0c1d2e3f405")

type channelParser func(raw any, env map[string]string) (parsedInbound, error)

type channelVerifier func(h http.Header, body []byte, env map[string]string) bool

// channelInbound holds the inbound half of each built-in channel: ingress is
// how an update reaches OpenNeko when no public webhook URL is set.
var channelInbound = map[string]struct {
	ingress string
	parse   channelParser
	verify  channelVerifier
}{
	"slack":    {"socket", parseSlackUpdate, verifySlackRequest},
	"telegram": {"webhook", parseTelegramUpdate, verifyTelegramRequest},
	"hud":      {"webhook", parseHUDUpdate, verifyHUDRequest},
}

func (a *API) channelEnv(id string) (map[string]string, error) {
	store, err := secrets.Read(a.SecretsDir)
	if err != nil {
		return nil, err
	}
	return store["channel."+id], nil
}

// inboundUpdateKey is inboundUpdateKey(): SHA-256 of the canonical JSON.
func inboundUpdateKey(raw any) string {
	sum := sha256.Sum256([]byte(jsCanonical(raw)))
	return hex.EncodeToString(sum[:])
}

// beginInbound claims an update. A new or pending update proceeds; a done or
// dead one does not.
func (a *API) beginInbound(ctx context.Context, orgID, channel, key string) (proceed, dead bool, err error) {
	var status string
	err = a.DB.QueryRow(ctx, `with claimed as (
		  insert into inbound_dedup (org_id, channel_plugin, update_key, status, attempts)
		  values ($1, $2, $3, 'pending', 0) on conflict do nothing returning status)
		select status from claimed
		union all select status from inbound_dedup where org_id = $1 and channel_plugin = $2 and update_key = $3
		limit 1`, orgID, channel, key).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return status == "pending", status == "dead", nil
}

func (a *API) markInboundDone(ctx context.Context, orgID, channel, key string) error {
	_, err := a.DB.Exec(ctx, `update inbound_dedup set status = 'done', updated_at = now()
		where org_id = $1 and channel_plugin = $2 and update_key = $3`, orgID, channel, key)
	return err
}

func (a *API) recordInboundFailure(ctx context.Context, orgID, channel, key string, raw any, failure string) (dead bool, attempts int, err error) {
	attempts = inboundMaxAttempts
	err = a.DB.QueryRow(ctx, `update inbound_dedup set attempts = attempts + 1, last_error = $4, updated_at = now(),
		  status = case when attempts + 1 >= $5 then 'dead' else status end,
		  payload = case when attempts + 1 >= $5 then $6::jsonb else payload end
		where org_id = $1 and channel_plugin = $2 and update_key = $3 returning attempts`,
		orgID, channel, key, failure, inboundMaxAttempts, jsStringify(raw, false)).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return attempts >= inboundMaxAttempts, attempts, err
}

func (a *API) loadPollCursor(ctx context.Context, orgID, channel string) (string, error) {
	var cursor string
	err := a.DB.QueryRow(ctx, `select cursor from channel_poll_cursor where org_id = $1 and channel_plugin = $2`, orgID, channel).Scan(&cursor)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return cursor, err
}

func (a *API) savePollCursor(ctx context.Context, orgID, channel, cursor string) error {
	_, err := a.DB.Exec(ctx, `insert into channel_poll_cursor (org_id, channel_plugin, cursor) values ($1, $2, $3)
		on conflict (org_id, channel_plugin) do update set cursor = excluded.cursor, updated_at = now()`, orgID, channel, cursor)
	return err
}

// pruneInboundDedup drops done rows past the TTL. Dead rows stay for review.
func (a *API) pruneInboundDedup(ctx context.Context, now time.Time) (int64, error) {
	tag, err := a.DB.Exec(ctx, `delete from inbound_dedup where status = 'done' and created_at < $1`, now.Add(-inboundDedupTTL))
	return tag.RowsAffected(), err
}

// processInbound parses and dispatches one update once. It returns false
// only while a failed update still has attempts left, so the caller holds
// its poll cursor.
func (a *API) processInbound(ctx context.Context, orgID, channel string, raw any) bool {
	key := inboundUpdateKey(raw)
	proceed, dead, err := a.beginInbound(ctx, orgID, channel, key)
	if err != nil {
		log.Printf("[channel-inbound] %s: could not claim the update: %v", channel, err)
		return false
	}
	if !proceed {
		kind := "duplicate"
		if dead {
			kind = "dead-lettered"
		}
		log.Printf("[channel-inbound] %s: skipping %s inbound update", channel, kind)
		return true
	}
	if err = a.dispatchInbound(ctx, orgID, channel, raw); err == nil {
		if err = a.markInboundDone(ctx, orgID, channel, key); err == nil {
			return true
		}
	}
	dead, attempts, ferr := a.recordInboundFailure(context.WithoutCancel(ctx), orgID, channel, key, raw, err.Error())
	if ferr != nil {
		log.Printf("[channel-inbound] %s: could not record the failure: %v", channel, ferr)
	}
	if dead {
		log.Printf("[channel-inbound] %s: dead-lettered update after %d attempt(s): %v", channel, attempts, err)
		return true
	}
	log.Printf("[channel-inbound] %s dispatch error (attempt %d/%d): %v", channel, attempts, inboundMaxAttempts, err)
	return false
}

func (a *API) dispatchInbound(ctx context.Context, orgID, channel string, raw any) error {
	in, ok := channelInbound[channel]
	if !ok {
		return fmt.Errorf("unknown channel %s", channel)
	}
	env, err := a.channelEnv(channel)
	if err != nil {
		return err
	}
	parsed, err := in.parse(raw, env)
	if err != nil {
		return err
	}
	workspace, _ := jsGet(parsed.Sender, "workspaceId").(string)
	org, err := a.resolveInboundOrg(ctx, channel, workspace, orgID)
	if err != nil {
		return err
	}
	if parsed.Recipient != nil {
		if err := a.ensureInboundBinding(ctx, org, channel, parsed.Recipient); err != nil {
			return err
		}
	}
	for _, intent := range parsed.Intents {
		if err := a.dispatchIntent(ctx, org, channel, intent, parsed.Recipient, parsed.Sender); err != nil {
			return err
		}
	}
	return nil
}

// resolveInboundOrg maps a sender's workspace to its org. An unknown
// workspace binds to the default org on first contact.
func (a *API) resolveInboundOrg(ctx context.Context, channel, workspace, defaultOrg string) (string, error) {
	if workspace == "" {
		return defaultOrg, nil
	}
	var org string
	err := a.DB.QueryRow(ctx, `select org_id from channel_workspace where channel_plugin = $1 and workspace_id = $2`, channel, workspace).Scan(&org)
	if err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return org, err
	}
	tag, err := a.DB.Exec(ctx, `insert into channel_workspace (org_id, channel_plugin, workspace_id) values ($1, $2, $3) on conflict do nothing`,
		defaultOrg, channel, workspace)
	if err == nil && tag.RowsAffected() > 0 {
		log.Printf("[channel-inbound] auto-bound workspace %s/%s → org %s", channel, workspace, defaultOrg)
	}
	return defaultOrg, err
}

// ensureInboundBinding binds delivery to the first sender of a channel, so
// outputs reach the operator who wrote first.
func (a *API) ensureInboundBinding(ctx context.Context, orgID, channel string, recipient *jsObj) error {
	tag, err := a.DB.Exec(ctx, `insert into delivery_binding (org_id, channel_plugin, recipient, enabled)
		select $1, $2, $3::jsonb, true
		where not exists (select 1 from delivery_binding where org_id = $1 and channel_plugin = $2)`,
		orgID, channel, jsStringify(recipient, false))
	if err == nil && tag.RowsAffected() > 0 {
		log.Printf("[channel-inbound] auto-bound %s → %s (first inbound from this sender)", channel, jsStringify(recipient, false))
	}
	return err
}

type channelActor struct {
	UserID  string
	Admin   bool
	Blocked bool
}

func (c channelActor) role() string {
	if c.Admin {
		return "admin"
	}
	return "member"
}

// resolveChannelActor is resolveChannelActor() in the worker: a linked
// identity acts as its user, an email match links once, anyone else is an
// anonymous member, and a blocked identity is dropped.
func (a *API) resolveChannelActor(ctx context.Context, orgID, channel string, sender *jsObj) (channelActor, error) {
	if sender == nil {
		return channelActor{}, nil
	}
	text := func(k string) string { s, _ := jsGet(sender, k).(string); return s }
	senderID, workspace, name, email := text("id"), text("workspaceId"), text("displayName"), text("email")
	var id, status string
	var userID, storedEmail *string
	err := a.DB.QueryRow(ctx, `with seen as (
		  insert into channel_identity (org_id, channel_plugin, workspace_id, channel_user_id, display_name, email)
		  values ($1, $2, $3, $4, $5, $6) on conflict do nothing returning id)
		update channel_identity set
		  display_name = coalesce(nullif($5, ''), display_name),
		  email = coalesce(nullif($6, ''), email),
		  updated_at = case when (nullif($5, '') is not null and $5 is distinct from display_name)
		    or (nullif($6, '') is not null and $6 is distinct from email) then now() else updated_at end
		where org_id = $1 and channel_plugin = $2 and workspace_id = $3 and channel_user_id = $4 and not exists (select 1 from seen)
		returning id::text, status, app_user_id, email`,
		orgID, channel, workspace, senderID, nullable(name), nullable(email)).Scan(&id, &status, &userID, &storedEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		err = a.DB.QueryRow(ctx, `select id::text, status, app_user_id, email from channel_identity
			where org_id = $1 and channel_plugin = $2 and workspace_id = $3 and channel_user_id = $4`,
			orgID, channel, workspace, senderID).Scan(&id, &status, &userID, &storedEmail)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return channelActor{}, nil
	}
	if err != nil {
		return channelActor{}, err
	}
	if status == "blocked" {
		return channelActor{Blocked: true}, nil
	}
	if status == "linked" && userID != nil {
		var active bool
		if err := a.DB.QueryRow(ctx, `select exists (select 1 from app_user where org_id = $1 and id = $2 and disabled_at is null)`,
			orgID, *userID).Scan(&active); err != nil || !active {
			return channelActor{}, err
		}
		return a.linkedChannelActor(ctx, orgID, channel, *userID)
	}
	if email == "" && storedEmail != nil {
		email = *storedEmail
	}
	if email == "" {
		return channelActor{}, nil
	}
	var match string
	err = a.DB.QueryRow(ctx, `select id from app_user where org_id = $1 and lower(email) = lower($2) and disabled_at is null limit 1`,
		orgID, email).Scan(&match)
	if errors.Is(err, pgx.ErrNoRows) {
		return channelActor{}, nil
	}
	if err != nil {
		return channelActor{}, err
	}
	if _, err := a.DB.Exec(ctx, `update channel_identity set app_user_id = $2, status = 'linked', verified_at = now(), updated_at = now()
		where id::text = $1`, id, match); err != nil {
		return channelActor{}, err
	}
	log.Printf("[channel-identity] auto-linked %s/%s → %s (email match)", channel, senderID, match)
	return a.linkedChannelActor(ctx, orgID, channel, match)
}

// linkedChannelActor drops a linked user who does not hold the channel.
func (a *API) linkedChannelActor(ctx context.Context, orgID, channel, userID string) (channelActor, error) {
	role, err := a.role(ctx, orgID, userID)
	if err != nil {
		return channelActor{}, err
	}
	held, err := a.heldItems(ctx, actor{OrgID: orgID, UserID: userID, Admin: role == "admin"}, "channel")
	if err != nil {
		return channelActor{}, err
	}
	if !held.has(channel) {
		return channelActor{Blocked: true}, nil
	}
	return channelActor{UserID: userID, Admin: role == "admin"}, nil
}

func (a *API) dispatchIntent(ctx context.Context, orgID, channel string, intent, recipient, sender *jsObj) error {
	who, err := a.resolveChannelActor(ctx, orgID, channel, sender)
	if err != nil {
		return err
	}
	if who.Blocked {
		log.Printf("[channel-inbound] dropped inbound from blocked identity %s/%s", channel, jsStr(jsGet(sender, "id")))
		return nil
	}
	text := func(k string) string { s, _ := jsGet(intent, k).(string); return s }
	switch text("kind") {
	case "decision":
		return a.channelDecision(ctx, orgID, who, text("decisionRef"), text("choice"), jsGet(intent, "reason"))
	case "utterance":
		return a.startChannelRun(ctx, orgID, channel, text("text"), text("threadRef"), recipient, sender, who)
	case "invoke":
		message := "/" + text("command")
		if args := jsGet(intent, "args"); jsTruthy(jsMember(args, "text")) {
			message += " " + jsStr(jsGet(args, "text"))
		}
		return a.startChannelRun(ctx, orgID, channel, message, "", recipient, sender, who)
	}
	return a.startChannelRun(ctx, orgID, channel, text("optionId"), "", recipient, sender, who)
}

// channelDecision approves or rejects a pending request. A refused decision
// is logged and settled: a retry cannot change the answer.
func (a *API) channelDecision(ctx context.Context, orgID string, who channelActor, ref, choice string, reason any) error {
	var status string
	err := error(pgx.ErrNoRows)
	if isUUID(ref) {
		err = a.DB.QueryRow(ctx, `select status from action_request where org_id = $1 and id = $2`, orgID, ref).Scan(&status)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		log.Printf("[channel-inbound] decision for unknown action_request %s", ref)
		return nil
	}
	if err != nil {
		return err
	}
	if status != "pending_approval" && status != "draft" {
		log.Printf("[channel-inbound] action_request %s already %s; ignoring duplicate %s", ref, status, choice)
		return nil
	}
	var why *string
	if s, ok := reason.(string); ok {
		why = &s
	}
	status, err = a.applyDecision(ctx, actor{OrgID: orgID, UserID: who.UserID, Admin: who.Admin}, ref, choice, why)
	var refused *decisionError
	if errors.As(err, &refused) {
		log.Printf("[channel-inbound] %s of action_request %s refused: %s", choice, ref, refused.message)
		return nil
	}
	if err == nil {
		log.Printf("[channel-inbound] %s action_request %s", status, ref)
	}
	return err
}

// conversationKey is conversationKeyFor(): one thread per chat and thread.
func conversationKey(recipient *jsObj, threadRef string) string {
	base := ""
	if s, _ := jsGet(recipient, "channel").(string); s != "" {
		base = s
	} else if v := jsGet(recipient, "chatId"); v != nil && jsStr(v) != "" {
		base = jsStr(v)
	} else if s, _ := jsGet(recipient, "to").(string); s != "" {
		base = s
	}
	if base == "" || threadRef == "" {
		return base
	}
	return base + ":" + threadRef
}

func channelThreadID(orgID, channel, key string) string {
	return uuid.NewSHA1(channelThreadNS, []byte(orgID+":"+channel+":"+key)).String()
}

// startChannelRun is startChatRun() in the worker: the channel thread, the
// run with its spend check, the user message and the work_run job.
func (a *API) startChannelRun(ctx context.Context, orgID, channel, message, threadRef string, recipient, sender *jsObj, who channelActor) error {
	title := strings.ToUpper(channel[:1]) + channel[1:]
	if threadRef != "" {
		title += " " + threadRef
	}
	key := ""
	if recipient != nil {
		key = conversationKey(recipient, threadRef)
	}
	var threadID string
	if jsTruthy(jsMember(recipient, "requireThread")) {
		if key == "" {
			return nil
		}
		var exists bool
		if err := a.DB.QueryRow(ctx, `select exists (select 1 from work_thread where org_id = $1 and id = $2)`,
			orgID, channelThreadID(orgID, channel, key)).Scan(&exists); err != nil || !exists {
			return err
		}
	}
	if key != "" {
		threadID = channelThreadID(orgID, channel, key)
		if _, err := a.DB.Exec(ctx, `insert into work_thread (id, org_id, title, channel, created_by_user_id)
			values ($1, $2, $3, $4, $5) on conflict do nothing`, threadID, orgID, title, channel, nullable(who.UserID)); err != nil {
			return err
		}
	} else if err := a.DB.QueryRow(ctx, `insert into work_thread (org_id, title, channel, created_by_user_id) values ($1, $2, $3, $4) returning id::text`,
		orgID, title, channel, nullable(who.UserID)).Scan(&threadID); err != nil {
		return err
	}
	runID, err := a.createWorkRun(ctx, orgID, threadID, who.UserID, who.role(), "channel", "")
	var over *spendExceeded
	if errors.As(err, &over) && recipient != nil {
		reply := fmt.Sprintf("spend-%s-%d", threadID, time.Now().UnixMilli())
		return a.queueChannelDelivery(ctx, orgID, channel, []byte(jsStringify(recipient, false)),
			[]any{jsObjOf("kind", "converse", "id", reply, "role", "assistant", "text", over.Error())}, "reply-"+reply)
	}
	if err != nil {
		return err
	}
	if err := a.saveUserMessage(ctx, orgID, threadID, runID, message); err != nil {
		return err
	}
	payload := jsObjOf("message", message)
	args := jobs.WorkRunArgs{OrgID: orgID, RunID: runID, ThreadID: threadID, Message: message, Channel: channel, ChannelPlugin: channel}
	if recipient != nil {
		args.Recipient = []byte(jsStringify(recipient, false))
	}
	if sender != nil {
		payload.set("sender", sender)
		args.Sender = []byte(jsStringify(sender, false))
	}
	queued := float64(time.Now().UnixMilli())
	args.QueuedAt = &queued
	if a.Jobs == nil {
		return errors.New("the job queue is not ready")
	}
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `insert into processing_job (org_id, kind, status, trigger, trigger_payload)
			values ($1, 'work_run', 'queued', 'channel_inbound', $2::jsonb) returning id::text`,
			orgID, jsStringify(payload, false)).Scan(&args.ProcessingJobID); err != nil {
			return err
		}
		return a.Jobs.WorkRun(ctx, tx, args)
	})
	if err == nil {
		log.Printf("[channel-inbound] started chat run %s for %q", runID, jsSlice(message, 40))
	}
	return err
}

// channelWebhook is POST /api/v1/channels/{channelId}/inbound: a provider
// webhook, checked by the channel's own signature rule.
func (a *API) channelWebhook(w http.ResponseWriter, r *http.Request) {
	ch := findBuiltInChannel(r.PathValue("channelId"))
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInboundBody+1))
	if err != nil || len(body) > maxInboundBody {
		sendError(w, http.StatusBadRequest, "could not read request body")
		return
	}
	refuse := func() { sendJSON(w, http.StatusOK, map[string]any{"ok": false, "dispatched": 0}) }
	if ch == nil {
		refuse()
		return
	}
	env, err := a.channelEnv(ch.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !channelEnabled(ch, env) || !channelInbound[ch.ID].verify(r.Header, body, env) {
		refuse()
		return
	}
	raw, err := jsParse(body)
	if err != nil {
		refuse()
		return
	}
	orgID, err := a.OrgID(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ok := a.processInbound(context.WithoutCancel(r.Context()), orgID, ch.ID, raw)
	dispatched := 0
	if ok {
		dispatched = 1
	}
	sendJSON(w, http.StatusOK, map[string]any{"ok": ok, "dispatched": dispatched})
}
