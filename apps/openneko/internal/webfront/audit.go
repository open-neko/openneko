package webfront

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
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

func (a *API) reportAuditFailure(e auditEvent, err error) {
	alert, _ := json.Marshal(map[string]any{
		"type":       "security.audit_logging_failure",
		"severity":   "critical",
		"occurredAt": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"orgId":      e.OrgID,
		"entityKind": e.EntityKind,
		"entityId":   e.EntityID,
		"event":      e.Event,
		"error":      err.Error(),
	})
	fmt.Fprintln(os.Stderr, string(alert))
	url := a.AuditWebhook
	if url == "" {
		url = strings.TrimSpace(os.Getenv("OPENNEKO_AUDIT_FAILURE_WEBHOOK_URL"))
	}
	if url == "" {
		return
	}
	token := strings.TrimSpace(os.Getenv("OPENNEKO_AUDIT_FAILURE_WEBHOOK_TOKEN"))
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(alert))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, `{"type":"security.audit_logging_failure_webhook_error","severity":"critical","error":%q}`+"\n", err.Error())
			return
		}
		res.Body.Close()
	}()
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// canonicalJSON matches canonicalJson() in packages/llm/src/canonical-json.ts
// for strings, booleans, nil, integers, slices and maps with ASCII keys.
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
		sort.Strings(keys)
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
