package webfront

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/secretenv"
)

const genesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

type auditEvent struct {
	OrgID      string
	EntityKind string
	EntityID   string
	Event      string
	Payload    map[string]any
}

// recordAudit appends to the hash chain of
// packages/llm/src/workflows/audit-chain.ts. A failed append must not fail
// the operation; it is reported on stderr and to the failure webhook.
func (a *API) recordAudit(ctx context.Context, e auditEvent) {
	if err := a.appendAudit(ctx, e); err != nil {
		a.reportAuditFailure(e, err)
	}
}

func (a *API) appendAudit(ctx context.Context, e auditEvent) error {
	canonical, err := canonicalJSON(e.Payload)
	if err != nil {
		return err
	}
	payloadHash := sha256Hex(canonical)
	return pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, "audit:"+e.OrgID); err != nil {
			return err
		}
		seq, prev := 0, genesisHash
		err := tx.QueryRow(ctx, `select seq, chain_hash from audit_chain where org_id = $1 order by seq desc limit 1`, e.OrgID).Scan(&seq, &prev)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		seq++
		chain := sha256Hex(prev + "\x00" + strconv.Itoa(seq) + "\x00" + payloadHash)
		_, err = tx.Exec(ctx, `
			insert into audit_chain (org_id, seq, entity_kind, entity_id, event, payload, payload_hash, prev_hash, chain_hash)
			values ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9)`,
			e.OrgID, seq, e.EntityKind, e.EntityID, e.Event, canonical, payloadHash, prev, chain)
		return err
	})
}

type auditFailure struct {
	OccurredAt string `json:"occurredAt"`
	OrgID      string `json:"orgId"`
	EntityKind string `json:"entityKind"`
	EntityID   string `json:"entityId"`
	Event      string `json:"event"`
	Error      string `json:"error"`
}

// AuditHealth is getAuditLoggingHealth() of the Node audit chain.
type AuditHealth struct {
	Healthy       bool    `json:"healthy"`
	FailureCount  int     `json:"failureCount"`
	LastFailureAt *string `json:"lastFailureAt"`
	LastError     *string `json:"lastError"`
}

// AuditHealth returns the audit logging health state for /health/security.
func (a *API) AuditHealth() AuditHealth {
	if a == nil {
		return AuditHealth{Healthy: true}
	}
	a.auditMu.Lock()
	defer a.auditMu.Unlock()
	h := AuditHealth{Healthy: a.auditFailures == 0, FailureCount: a.auditFailures}
	if a.auditFailures > 0 && a.lastAuditFailure != nil {
		at, msg := a.lastAuditFailure.OccurredAt, a.lastAuditFailure.Error
		h.LastFailureAt, h.LastError = &at, &msg
	}
	return h
}

// ResetAuditHealth clears the recorded audit logging failure count.
func (a *API) ResetAuditHealth() {
	if a == nil {
		return
	}
	a.auditMu.Lock()
	a.auditFailures = 0
	a.lastAuditFailure = nil
	a.auditMu.Unlock()
}

func (a *API) reportAuditFailure(e auditEvent, err error) {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	a.auditMu.Lock()
	a.auditFailures++
	a.lastAuditFailure = &auditFailure{
		OccurredAt: now,
		OrgID:      e.OrgID,
		EntityKind: e.EntityKind,
		EntityID:   e.EntityID,
		Event:      e.Event,
		Error:      err.Error(),
	}
	a.auditMu.Unlock()

	alert, _ := json.Marshal(map[string]any{
		"type":       "security.audit_logging_failure",
		"severity":   "critical",
		"occurredAt": now,
		"orgId":      e.OrgID,
		"entityKind": e.EntityKind,
		"entityId":   e.EntityID,
		"event":      e.Event,
		"error":      err.Error(),
	})
	fmt.Fprintln(auditStderr, string(alert))
	hook := a.auditWebhookURL()
	if hook == "" {
		return
	}
	host := auditWebhookHost(hook)
	if err := auditWebhookAllowed(hook); err != nil {
		a.reportAuditDeliveryFailure(e, host, err)
		return
	}
	token := strings.TrimSpace(secretenv.Get("OPENNEKO_AUDIT_FAILURE_WEBHOOK_TOKEN"))
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), auditWebhookTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook, bytes.NewReader(alert))
		if err != nil {
			a.reportAuditDeliveryFailure(e, host, errors.New("the request could not be made"))
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := auditWebhookClient.Do(req)
		if err != nil {
			a.reportAuditDeliveryFailure(e, host, auditTransportError(err))
			return
		}
		res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode > 299 {
			a.reportAuditDeliveryFailure(e, host, fmt.Errorf("HTTP %d", res.StatusCode))
		}
	}()
}

var (
	auditWebhookClient            = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	auditWebhookTimeout           = 2 * time.Second
	auditStderr         io.Writer = os.Stderr
)

func (a *API) auditWebhookURL() string {
	if a == nil {
		return ""
	}
	if a.AuditWebhook != "" {
		return a.AuditWebhook
	}
	return strings.TrimSpace(secretenv.Get("OPENNEKO_AUDIT_FAILURE_WEBHOOK_URL"))
}

// auditWebhookHost is the only part of the URL that OpenNeko logs: the path
// or the query may hold a token.
func auditWebhookHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "an unreadable URL"
	}
	return u.Host
}

func auditTransportError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("no reply in %s", auditWebhookTimeout)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func auditWebhookAllowed(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("the URL does not parse")
	}
	if u.Host == "" || (u.Scheme != "https" && (u.Scheme != "http" || !isLoopbackHostname(u.Hostname()))) {
		return errors.New("the URL must be https, or http on localhost")
	}
	return nil
}

// AuditWebhookState is the audit failure webhook as /health/security shows
// it to an admin. State is "off", "ok" or "refused".
type AuditWebhookState struct {
	State string  `json:"state"`
	Host  *string `json:"host"`
	Error *string `json:"error"`
}

func (a *API) AuditWebhookState() AuditWebhookState {
	hook := a.auditWebhookURL()
	if hook == "" {
		return AuditWebhookState{State: "off"}
	}
	host := auditWebhookHost(hook)
	if err := auditWebhookAllowed(hook); err != nil {
		msg := err.Error()
		return AuditWebhookState{State: "refused", Host: &host, Error: &msg}
	}
	return AuditWebhookState{State: "ok", Host: &host}
}

// CheckAuditWebhook writes one warning when the rule refuses the audit
// failure webhook URL.
func (a *API) CheckAuditWebhook(w io.Writer) {
	if s := a.AuditWebhookState(); s.State == "refused" {
		fmt.Fprintf(w, "openneko web: warning: OPENNEKO_AUDIT_FAILURE_WEBHOOK_URL (%s) is refused: %s. Audit failure alerts go to stderr only.\n", *s.Host, *s.Error)
	}
}

func (a *API) reportAuditDeliveryFailure(e auditEvent, host string, err error) {
	msg := "audit failure webhook (" + host + "): " + err.Error()
	a.auditMu.Lock()
	a.auditFailures++
	a.lastAuditFailure = &auditFailure{
		OccurredAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		OrgID:      e.OrgID,
		EntityKind: e.EntityKind,
		EntityID:   e.EntityID,
		Event:      e.Event,
		Error:      msg,
	}
	a.auditMu.Unlock()
	fmt.Fprintf(auditStderr, `{"type":"security.audit_logging_failure_webhook_error","severity":"critical","error":%q}`+"\n", msg)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// canonicalJSON matches canonicalJson() in packages/llm/src/canonical-json.ts.
// Decode stored JSON with UseNumber before you pass it here.
func canonicalJSON(v any) (string, error) {
	var b strings.Builder
	if err := writeCanonical(&b, v); err != nil {
		return "", err
	}
	return b.String(), nil
}

func writeCanonical(b *strings.Builder, v any) error {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case string:
		writeJSString(b, t)
	case int:
		b.WriteString(strconv.Itoa(t))
	case json.Number:
		out, err := jsJSON([]byte(t))
		if err != nil {
			return err
		}
		b.Write(out)
	case []any:
		b.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeCanonical(b, item); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			return slices.Compare(utf16.Encode([]rune(keys[i])), utf16.Encode([]rune(keys[j]))) < 0
		})
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSString(b, k)
			b.WriteByte(':')
			if err := writeCanonical(b, t[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("canonical JSON: unsupported %T", v)
	}
	return nil
}

// writeJSString writes a string as JSON.stringify does.
func writeJSString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}
