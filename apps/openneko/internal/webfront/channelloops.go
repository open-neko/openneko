package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

const (
	pollInterval      = 3 * time.Second
	pollMaxBackoff    = 60 * time.Second
	pollRepeatLogStep = 10
	channelRefresh    = 30 * time.Second
	dedupPruneEvery   = time.Hour
)

// pollBackoff is pollBackoffMs(): the base interval while healthy, doubled
// per failure up to one minute.
func pollBackoff(failures int) time.Duration {
	if failures <= 0 {
		return pollInterval
	}
	return min(pollInterval<<min(failures-1, 6), pollMaxBackoff)
}

// logPollFailure logs a new error, and each tenth repeat of the same one.
func logPollFailure(failures int, changed bool) bool {
	return changed || failures%pollRepeatLogStep == 0
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

type inboundLoop struct {
	cancel context.CancelFunc
	sig    string
}

type channelLoops struct {
	mu      sync.Mutex
	ctx     context.Context
	running map[string]*inboundLoop
	noted   map[string]string
	wake    chan struct{}
}

// StartChannelInbound runs the inbound loops of the turned-on channels until
// ctx ends. It follows the channel settings: a channel turned on starts, a
// channel turned off stops, and a changed setting restarts its loop.
func (a *API) StartChannelInbound(ctx context.Context) {
	l := &channelLoops{ctx: ctx, running: map[string]*inboundLoop{}, noted: map[string]string{}, wake: make(chan struct{}, 1)}
	a.inboundMu.Lock()
	a.inbound = l
	a.inboundMu.Unlock()
	go func() {
		refresh := time.NewTicker(channelRefresh)
		prune := time.NewTicker(dedupPruneEvery)
		defer refresh.Stop()
		defer prune.Stop()
		for {
			a.refreshInbound(l)
			select {
			case <-ctx.Done():
				return
			case <-l.wake:
			case <-refresh.C:
			case <-prune.C:
				if n, err := a.pruneInboundDedup(ctx, time.Now()); err != nil {
					log.Printf("[channel-inbound] dedup prune error: %v", err)
				} else if n > 0 {
					log.Printf("[channel-inbound] pruned %d expired dedup row(s)", n)
				}
			}
		}
	}()
}

// channelsChanged asks the inbound loops to read the settings again.
func (a *API) channelsChanged() {
	a.inboundMu.Lock()
	l := a.inbound
	a.inboundMu.Unlock()
	if l == nil {
		return
	}
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func envSignature(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + env[k] + "\x00")
	}
	return b.String()
}

func (a *API) inboundPublicURL() string {
	u := a.PublicURL
	if u == "" {
		u = os.Getenv("OPENNEKO_PUBLIC_URL")
	}
	return strings.TrimRight(strings.TrimSpace(u), "/")
}

// existingOrgID is the org that OrgID would return, without the side effect
// of creating one on an empty install.
// telegramWebhookOn is true when Telegram can deliver to the webhook route:
// the admin stored the secret_token of setWebhook, and the public URL is
// https on a host that Telegram can reach. Else OpenNeko polls.
func telegramWebhookOn(public string, env map[string]string) bool {
	return env["TELEGRAM_WEBHOOK_SECRET"] != "" && reachablePublicURL(public)
}

func reachablePublicURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if ip := net.ParseIP(host); ip != nil {
		return !webhookAddressBlocked(ip) && !ip.IsPrivate() && !cgnatNet.Contains(ip)
	}
	if !strings.Contains(host, ".") || host == "localhost" {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".lan", ".home.arpa"} {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	return true
}

func (a *API) existingOrgID(ctx context.Context) (string, error) {
	a.orgMu.Lock()
	cached := a.orgID
	a.orgMu.Unlock()
	if cached != "" {
		return cached, nil
	}
	var id string
	err := a.DB.QueryRow(ctx, `select id from organization order by created_at, id limit 1`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func telegramInboundURL(public string) string {
	return public + "/api/v1/channels/telegram/inbound"
}

// telegramWebhookURL is the URL that getWebhookInfo reports for the bot; ""
// means that the bot has no webhook.
func telegramWebhookURL(ctx context.Context, env map[string]string) (string, error) {
	token := env["TELEGRAM_BOT_TOKEN"]
	if token == "" {
		return "", errors.New("no TELEGRAM_BOT_TOKEN")
	}
	raw, status, err := postChannelBody(ctx, telegramAPIBase+"/bot"+token+"/getWebhookInfo", nil, []byte("{}"), telegramPollTimeout)
	if err != nil {
		return "", fmt.Errorf("Telegram getWebhookInfo network error: %s", strings.ReplaceAll(err.Error(), token, "<token>"))
	}
	envelope, _ := jsParse(raw)
	if jsGet(envelope, "ok") != true {
		return "", fmt.Errorf("Telegram getWebhookInfo failed (HTTP %d)", status)
	}
	u, _ := jsGet(jsGet(envelope, "result"), "url").(string)
	return u, nil
}

// telegramIngress is "webhook" only when the settings allow the webhook and
// Telegram reports this install's inbound URL for the bot. Else it is
// "poll", with the reason to log.
func (a *API) telegramIngress(ctx context.Context, public string, env map[string]string) (string, string) {
	if !telegramWebhookOn(public, env) {
		return "poll", ""
	}
	want := telegramInboundURL(public)
	got, err := telegramWebhookURL(ctx, env)
	switch {
	case err != nil:
		return "poll", fmt.Sprintf("could not check the Telegram webhook (%v); polling", err)
	case got == "":
		return "poll", "TELEGRAM_WEBHOOK_SECRET is set, but the bot has no webhook; polling. To use the webhook, call setWebhook with url " + want + " and the secret_token"
	case got != want:
		return "poll", "the bot's webhook points at another URL, not " + want + "; polling. Call setWebhook with this URL, or deleteWebhook"
	}
	return "webhook", ""
}

func (a *API) refreshInbound(l *channelLoops) {
	store := secrets.ReadSoft(a.SecretsDir, func(msg string) { log.Printf("channels: %s", msg) })
	public := a.inboundPublicURL()
	wanted, ingress, reason := map[string]string{}, map[string]string{}, map[string]string{}
	for _, ch := range a.enabledChannels() {
		mode := channelInbound[ch.ID].ingress
		if ch.ID == "telegram" {
			mode, reason[ch.ID] = a.telegramIngress(l.ctx, public, store["channel."+ch.ID])
		}
		ingress[ch.ID] = mode
		wanted[ch.ID] = envSignature(store["channel."+ch.ID]) + "ingress=" + mode
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	orgID := ""
	if len(wanted) > 0 {
		id, err := a.existingOrgID(l.ctx)
		if err != nil {
			log.Printf("[channel-inbound] start failed: %v", err)
			return
		}
		orgID = id
	}
	for id, loop := range l.running {
		if sig, ok := wanted[id]; ok && sig == loop.sig {
			continue
		}
		loop.cancel()
		delete(l.running, id)
		if _, ok := wanted[id]; !ok {
			log.Printf("[channel-inbound] %s: turned off — inbound stopped", id)
		}
	}
	for id := range l.noted {
		if _, ok := wanted[id]; !ok {
			delete(l.noted, id)
		}
	}
	for _, ch := range builtInChannels {
		sig, ok := wanted[ch.ID]
		if !ok || l.running[ch.ID] != nil {
			continue
		}
		note := sig + "\x00" + reason[ch.ID]
		if l.noted[ch.ID] != note {
			l.noted[ch.ID] = note
			if ingress[ch.ID] == "webhook" {
				log.Printf("[channel-inbound] %s: webhook ingress at %s/api/v1/channels/%s/inbound", ch.ID, public, ch.ID)
			} else if reason[ch.ID] != "" {
				log.Printf("[channel-inbound] %s: %s", ch.ID, reason[ch.ID])
			}
		}
		if ingress[ch.ID] == "webhook" || orgID == "" {
			continue
		}
		ctx, cancel := context.WithCancel(l.ctx)
		l.running[ch.ID] = &inboundLoop{cancel: cancel, sig: sig}
		if ingress[ch.ID] == "socket" {
			go a.slackSocketLoop(ctx, orgID, ch.ID)
		} else {
			go a.telegramPollLoop(ctx, orgID, ch.ID)
		}
	}
}

var telegramPollTimeout = channelSendTimeout

// pollTelegram is pollTelegram(): one getUpdates batch and the next offset.
func pollTelegram(ctx context.Context, cursor string, env map[string]string) ([]any, string, error) {
	token := env["TELEGRAM_BOT_TOKEN"]
	if token == "" {
		return nil, cursor, nil
	}
	body := jsObjOf("timeout", 0.0)
	if offset, err := strconv.ParseFloat(cursor, 64); cursor != "" && err == nil {
		body.set("offset", offset)
	}
	raw, status, err := postChannelBody(ctx, telegramAPIBase+"/bot"+token+"/getUpdates", nil, []byte(jsStringify(body, false)), telegramPollTimeout)
	if err != nil {
		return nil, cursor, fmt.Errorf("Telegram getUpdates network error: %s", strings.ReplaceAll(err.Error(), token, "<token>"))
	}
	envelope, perr := jsParse(raw)
	if len(raw) > 0 && perr != nil {
		return nil, cursor, fmt.Errorf("Telegram getUpdates returned non-JSON (HTTP %d): %s", status, jsSlice(string(raw), 200))
	}
	if jsGet(envelope, "ok") != true {
		desc, _ := jsGet(envelope, "description").(string)
		if desc == "" {
			desc = "no description"
		}
		if status == http.StatusConflict && strings.Contains(desc, "webhook") {
			return nil, cursor, fmt.Errorf("Telegram refuses the poll because the bot has a webhook (%s). Call deleteWebhook for the bot, or store TELEGRAM_WEBHOOK_SECRET and set OPENNEKO_PUBLIC_URL to the public https address of this install and call setWebhook with its /api/v1/channels/telegram/inbound URL", desc)
		}
		if status == http.StatusConflict {
			return nil, cursor, fmt.Errorf("Telegram refuses the poll because another program polls this bot (%s). Stop the other program or give this install its own bot", desc)
		}
		return nil, cursor, fmt.Errorf("Telegram getUpdates returned ok=false (%s)", desc)
	}
	updates, _ := jsGet(envelope, "result").([]any)
	maxID := -1.0
	for _, u := range updates {
		if id, ok := jsGet(u, "update_id").(float64); ok && id > maxID {
			maxID = id
		}
	}
	if maxID >= 0 {
		cursor = jsNumberText(maxID + 1)
	}
	return updates, cursor, nil
}

var pollLockRetry = 15 * time.Second

func pollLockKey(id string, env map[string]string) string {
	sum := sha256.Sum256([]byte(env["TELEGRAM_BOT_TOKEN"]))
	return "openneko:channel-poll:" + id + ":" + hex.EncodeToString(sum[:16])
}

// tryPollLock takes the bot's session advisory lock on a connection of its
// own. The lock lasts while that connection is open, so a process that dies
// lets another one take over.
func (a *API) tryPollLock(ctx context.Context, key string) (*pgx.Conn, error) {
	c, err := a.DB.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	conn := c.Hijack()
	var ok bool
	if err := conn.QueryRow(ctx, `select pg_try_advisory_lock(hashtextextended($1, 0))`, key).Scan(&ok); err != nil || !ok {
		_ = conn.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	return conn, nil
}

// telegramPollLoop is the worker's pollLoop(): the cursor moves only when
// the whole batch settled. One process on the database polls one bot.
func (a *API) telegramPollLoop(ctx context.Context, orgID, id string) {
	env, _ := a.channelEnv(id)
	key := pollLockKey(id, env)
	var lock *pgx.Conn
	defer func() {
		if lock != nil {
			_ = lock.Close(context.Background())
		}
	}()
	cursor, waiting := "", false
	failures, lastError := 0, ""
	for ctx.Err() == nil {
		if lock != nil && lock.Ping(ctx) != nil {
			_ = lock.Close(context.WithoutCancel(ctx))
			lock = nil
			log.Printf("[channel-inbound] %s: lost the poll lock", id)
		}
		if lock == nil {
			conn, err := a.tryPollLock(ctx, key)
			if conn == nil {
				if err != nil && ctx.Err() == nil {
					log.Printf("[channel-inbound] %s: could not take the poll lock: %v", id, err)
				} else if err == nil && !waiting {
					log.Printf("[channel-inbound] %s: another OpenNeko process polls this bot; this one waits", id)
				}
				waiting = err == nil
				if !sleepCtx(ctx, pollLockRetry) {
					return
				}
				continue
			}
			lock, waiting = conn, false
			if cursor, err = a.loadPollCursor(ctx, orgID, id); err != nil {
				log.Printf("[channel-inbound] %s: could not read the poll cursor: %v", id, err)
			}
			resumed := ""
			if cursor != "" {
				resumed = " — resumed from saved cursor"
			}
			log.Printf("[channel-inbound] %s: polling for inbound (webhook off)%s", id, resumed)
		}
		held, err := a.pollOnce(ctx, orgID, id, &cursor)
		switch {
		case err != nil && ctx.Err() == nil:
			failures++
			if logPollFailure(failures, err.Error() != lastError) {
				attempt := ""
				if failures > 1 {
					attempt = fmt.Sprintf(" (attempt %d)", failures)
				}
				log.Printf("[channel-inbound] %s poll error%s: %v", id, attempt, err)
			}
			lastError = err.Error()
		case held:
			failures++
			if logPollFailure(failures, failures == 1) {
				log.Printf("[channel-inbound] %s: holding cursor, retrying failed update(s) (attempt %d)", id, failures)
			}
		case failures > 0:
			log.Printf("[channel-inbound] %s: recovered after %d held/failed attempt(s)", id, failures)
			failures, lastError = 0, ""
		}
		if !sleepCtx(ctx, pollBackoff(failures)) {
			return
		}
	}
}

func (a *API) pollOnce(ctx context.Context, orgID, id string, cursor *string) (bool, error) {
	env, err := a.channelEnv(id)
	if err != nil {
		return false, err
	}
	updates, next, err := pollTelegram(ctx, *cursor, env)
	if err != nil {
		return false, err
	}
	held := false
	for _, u := range updates {
		if !a.processInbound(ctx, orgID, id, u) {
			held = true
		}
	}
	if !held && next != "" && next != *cursor {
		if err := a.savePollCursor(ctx, orgID, id, next); err != nil {
			return false, err
		}
		*cursor = next
	}
	return held, nil
}

// slackSocketLoop holds a Slack Socket Mode connection and reconnects with
// backoff. With no app token it warns once and ends.
func (a *API) slackSocketLoop(ctx context.Context, orgID, id string) {
	failures := 0
	for ctx.Err() == nil {
		env, err := a.channelEnv(id)
		if err == nil && env["SLACK_APP_TOKEN"] == "" {
			log.Printf("[channel-inbound] %s: no SLACK_APP_TOKEN — Slack inbound disabled. Enable Socket Mode and set an xapp- app-level token (connections:write).", id)
			return
		}
		if err == nil {
			err = a.slackSocketSession(ctx, orgID, id, env["SLACK_APP_TOKEN"])
		}
		if err != nil && ctx.Err() == nil {
			failures++
			log.Printf("[channel-inbound] %s socket error (attempt %d): %v", id, failures, err)
		} else {
			failures = 0
		}
		if !sleepCtx(ctx, pollBackoff(failures)) {
			return
		}
	}
}

func openSlackConnection(ctx context.Context, appToken string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, channelSendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, slackAPIBase+"/apps.connections.open", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+appToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var body struct {
		OK    bool   `json:"ok"`
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	if !body.OK || body.URL == "" {
		if body.Error == "" {
			body.Error = "no url returned"
		}
		return "", fmt.Errorf("apps.connections.open failed: %s", body.Error)
	}
	return body.URL, nil
}

// slackSocketSession reads one connection until Slack or ctx closes it. It
// acks each envelope before it handles the payload, as Slack asks within 3 s.
func (a *API) slackSocketSession(ctx context.Context, orgID, id, appToken string) error {
	wss, err := openSlackConnection(ctx, appToken)
	if err != nil {
		return err
	}
	conn, _, err := websocket.Dial(ctx, wss, nil)
	if err != nil {
		return err
	}
	defer conn.CloseNow() //nolint:errcheck
	conn.SetReadLimit(4 << 20)
	log.Printf("[channel-inbound] %s: Socket Mode connected", id)
	payloads := make(chan any, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for p := range payloads {
			a.processInbound(context.WithoutCancel(ctx), orgID, id, p)
		}
	}()
	defer func() { close(payloads); <-done }()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil || websocket.CloseStatus(err) != -1 {
				return nil
			}
			return err
		}
		frame, err := jsParse(data)
		if err != nil {
			continue
		}
		typ, _ := jsGet(frame, "type").(string)
		switch typ {
		case "hello":
			continue
		case "disconnect":
			return conn.Close(websocket.StatusNormalClosure, "")
		}
		if envelope, _ := jsGet(frame, "envelope_id").(string); envelope != "" {
			if err := conn.Write(ctx, websocket.MessageText, []byte(jsStringify(jsObjOf("envelope_id", envelope), false))); err != nil {
				return err
			}
		}
		if p := jsGet(frame, "payload"); p != nil && (typ == "events_api" || typ == "slash_commands" || typ == "interactive") {
			payloads <- p
		}
	}
}
