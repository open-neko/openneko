package webfront

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

// TestMagentoLiveAcceptance writes to a real Magento store. The install's
// own `openneko web` runs the queued jobs, so run it on that install's host
// with OPENNEKO_MAGENTO_LIVE_DATABASE_URL and the install's config folder in
// XDG_CONFIG_HOME.
func TestMagentoLiveAcceptance(t *testing.T) {
	dbURL := os.Getenv("OPENNEKO_MAGENTO_LIVE_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set OPENNEKO_MAGENTO_LIVE_DATABASE_URL to run against a live Magento store")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	inserter, err := jobs.NewInserter(pool, "")
	if err != nil {
		t.Fatal(err)
	}
	a := &API{DB: pool, Jobs: inserter}
	orgID, err := a.OrgID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var adminUserID string
	_ = pool.QueryRow(ctx, `select id from app_user where role = 'admin' and disabled_at is null limit 1`).Scan(&adminUserID)
	act := actor{OrgID: orgID, UserID: adminUserID, Admin: true}

	var rawConfig []byte
	if err := pool.QueryRow(ctx, `select config from pack_install where org_id = $1 and pack_id = 'magento' and status = 'installed'
		order by created_at desc limit 1`, orgID).Scan(&rawConfig); err != nil {
		t.Fatalf("Magento pack is not installed: %v", err)
	}
	var config map[string]any
	_ = json.Unmarshal(rawConfig, &config)
	store, err := secrets.Read(a.SecretsDir)
	if err != nil {
		t.Fatal(err)
	}
	token := store["pack.magento"]["MAGENTO_INTEGRATION_TOKEN"]
	if token == "" {
		t.Fatal("Magento Integration token is unavailable")
	}
	baseURL := strings.TrimRight(magentoConfigText(config["magento.base_url"], ""), "/")
	storeCode := magentoConfigText(config["magento.store_code"], "all")
	marker := fmt.Sprintf("openneko-v2-%x", time.Now().UnixMilli())

	magento := func(path string, out any) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/rest/"+url.PathEscape(storeCode)+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("Magento %s: HTTP %d", path, res.StatusCode)
		}
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	jsValue := func(v any) *jsObj {
		raw, _ := json.Marshal(v)
		parsed, _ := jsParse(raw)
		return parsed.(*jsObj)
	}
	management := func(body map[string]any) (map[string]any, error) {
		var out *jsObj
		var err error
		if body == nil {
			out, err = a.readMagentoStoreManagement(ctx, orgID)
		} else {
			in := jsValue(body)
			in.set("actorUserId", nullable(adminUserID))
			out, err = a.updateMagentoStoreManagement(ctx, orgID, in)
		}
		if err != nil {
			return nil, err
		}
		var decoded map[string]any
		_ = json.Unmarshal([]byte(jsStringify(out, false)), &decoded)
		return decoded, nil
	}
	actionInput := func(kind, scope, status, summary string, payload map[string]any) *jsObj {
		target := "magento_operator"
		if kind == "magento.financial_handoff" {
			target = "magento_admin_handoff"
		}
		return jsValue(map[string]any{
			"orgId": orgID, "actorUserId": nullable(adminUserID), "actorRole": "admin", "actorBackend": "magento-v2-live-acceptance",
			"scope": scope, "kind": kind, "target": target, "payload": payload, "riskLevel": "medium",
			"status": status, "summary": summary, "intent": summary,
		})
	}
	type receipt struct {
		changesetID string
		result      json.RawMessage
	}
	execute := func(in *jsObj, timeout time.Duration) (receipt, error) {
		created, err := a.createActionRequestInGo(ctx, in)
		if err != nil {
			return receipt{}, err
		}
		id, _ := created.vals["id"].(string)
		if status, _ := created.vals["status"].(string); status != "approved" {
			if _, err := a.applyDecision(ctx, act, http.MethodPost, id, "approve", nil); err != nil {
				return receipt{}, err
			}
		} else if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return a.enqueueActionExecute(ctx, tx, orgID, id) }); err != nil {
			return receipt{}, err
		}
		for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
			var status string
			var changesetID, execStatus, execError *string
			var result []byte
			if err := pool.QueryRow(ctx, `select r.status, r.payload->>'changeset_id', e.status, e.error, e.result
				  from action_request r left join lateral (select * from action_execution where action_request_id = r.id order by started_at desc limit 1) e on true
				 where r.org_id = $1 and r.id = $2::uuid`, orgID, id).Scan(&status, &changesetID, &execStatus, &execError, &result); err != nil {
				return receipt{}, err
			}
			switch status {
			case "executed":
				if execStatus == nil || *execStatus != "succeeded" {
					return receipt{}, fmt.Errorf("action %s executed without a successful receipt", id)
				}
				return receipt{deref(changesetID), result}, nil
			case "failed", "rejected":
				return receipt{}, fmt.Errorf("action %s %s: %s", id, status, deref(execError))
			}
		}
		return receipt{}, fmt.Errorf("action %s did not finish before the acceptance timeout", id)
	}
	mustExecute := func(in *jsObj, timeout time.Duration) receipt {
		t.Helper()
		r, err := execute(in, timeout)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	expectRejected := func(in *jsObj, pattern string) {
		t.Helper()
		_, err := a.createActionRequestInGo(ctx, in)
		if err == nil || !regexp.MustCompile(pattern).MatchString(err.Error()) {
			t.Fatalf("expected preparation rejection matching %s; got %v", pattern, err)
		}
	}
	undo := func(changesetID, suffix string) (receipt, error) {
		return execute(actionInput("magento.undo_changeset", "external", "pending_approval", "Acceptance inverse "+suffix,
			map[string]any{"changeset_id": changesetID, "idempotency_key": marker + "-undo-" + suffix}), 3*time.Minute)
	}
	mustUndo := func(changesetID, suffix string) receipt {
		t.Helper()
		r, err := undo(changesetID, suffix)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	var productSearch struct {
		Items []struct {
			SKU   string  `json:"sku"`
			Name  string  `json:"name"`
			Price float64 `json:"price"`
		} `json:"items"`
	}
	magento("/V1/products?searchCriteria[pageSize]=30", &productSearch)
	products := productSearch.Items[:0]
	for _, p := range productSearch.Items {
		if p.SKU != "" && p.Name != "" && p.Price > 0 {
			products = append(products, p)
		}
	}
	if len(products) < 3 {
		t.Fatal("Acceptance needs three priced Magento products")
	}
	var blockSearch struct {
		Items []struct {
			ID      float64 `json:"id"`
			Content string  `json:"content"`
		} `json:"items"`
	}
	magento("/V1/cmsBlock/search?searchCriteria[pageSize]=10", &blockSearch)
	var blockID float64
	var blockContent string
	for _, b := range blockSearch.Items {
		if b.ID > 0 {
			blockID, blockContent = b.ID, b.Content
			break
		}
	}
	if blockID == 0 {
		t.Fatal("Acceptance needs one Magento CMS block")
	}

	report := map[string]any{"profile": "magento-v2-local-acceptance", "startedAt": time.Now().UTC().Format(time.RFC3339)}
	var cleanup []string
	originalAutoExecute := false
	autoRuleID := ""
	defer func() {
		for i := len(cleanup) - 1; i >= 0; i-- {
			_, _ = undo(cleanup[i], fmt.Sprintf("cleanup-%d", len(cleanup)-1-i))
		}
		if autoRuleID != "" {
			_, _ = management(map[string]any{"action": "set_rule_status", "ruleId": autoRuleID, "enabled": false})
		}
		_, _ = management(map[string]any{"action": "update_domain", "domain": "catalog", "autoExecute": originalAutoExecute})
	}()

	initial, err := management(nil)
	if err != nil {
		t.Fatal(err)
	}
	controls, _ := initial["controls"].([]any)
	for _, c := range controls {
		if m, _ := c.(map[string]any); m["domain"] == "catalog" {
			originalAutoExecute, _ = m["autoExecute"].(bool)
		}
	}
	if _, err := management(map[string]any{"action": "update_domain", "domain": "catalog", "enabled": true, "autoExecute": true}); err != nil {
		t.Fatal(err)
	}
	ruleName := "Local acceptance " + marker
	withRule, err := management(map[string]any{
		"action": "create_rule", "name": ruleName,
		"instruction": "Exercise one reversible product-name correction, then stop at one action per day.",
		"domain":      "catalog", "actionKind": "magento.manage_catalog", "dailyCap": 1, "cooldownSeconds": 0,
		"enabled": true, "source": "acceptance_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	rules, _ := withRule["rules"].([]any)
	for _, r := range rules {
		if m, _ := r.(map[string]any); m["name"] == ruleName {
			autoRuleID, _ = m["id"].(string)
		}
	}
	if autoRuleID == "" {
		t.Fatal("Acceptance automatic rule was not persisted")
	}

	nameRow := func(sku, name string) map[string]any {
		return map[string]any{"entity_ref": sku, "path": map[string]any{"sku": sku},
			"body": map[string]any{"product": map[string]any{"sku": sku, "name": name + " [" + marker + "]"}}}
	}
	scope := map[string]any{"store": storeCode, "activityOrigin": "acceptance_test"}
	auto := mustExecute(actionInput("magento.manage_catalog", "external", "pending_approval", "Acceptance capped automatic catalog change", map[string]any{
		"operation": "product_update", "scope": scope, "auto_rule_id": autoRuleID, "idempotency_key": marker + "-auto-1",
		"rows": []any{nameRow(products[0].SKU, products[0].Name)},
	}), 3*time.Minute)
	if auto.changesetID == "" {
		t.Fatal("Automatic action did not create a change-set")
	}
	cleanup = append(cleanup, auto.changesetID)
	expectRejected(actionInput("magento.manage_catalog", "external", "pending_approval", "Acceptance automatic daily-cap rejection", map[string]any{
		"operation": "product_update", "scope": scope, "auto_rule_id": autoRuleID, "idempotency_key": marker + "-auto-2",
		"rows": []any{nameRow(products[1].SKU, products[1].Name)},
	}), `(?i)daily cap`)
	autoUndo := mustUndo(auto.changesetID, "auto")
	cleanup = cleanup[:len(cleanup)-1]
	report["automaticRule"] = map[string]any{"applied": auto.result, "capBreach": "rejected_before_execution", "inverse": autoUndo.result}

	var bulkRows []any
	for _, p := range products[1:3] {
		bulkRows = append(bulkRows, map[string]any{"entity_ref": p.SKU, "path": map[string]any{"sku": p.SKU},
			"body": map[string]any{"product": map[string]any{"sku": p.SKU, "price": math.Round(p.Price*1.01*100) / 100}}})
	}
	bulk := mustExecute(actionInput("magento.manage_catalog", "external", "pending_approval", "Acceptance two-product async bulk price change", map[string]any{
		"operation": "product_bulk_update", "scope": scope, "idempotency_key": marker + "-bulk-price", "rows": bulkRows,
	}), 4*time.Minute)
	if bulk.changesetID == "" {
		t.Fatal("Bulk action did not create a change-set")
	}
	cleanup = append(cleanup, bulk.changesetID)
	bulkUndo := mustUndo(bulk.changesetID, "bulk")
	cleanup = cleanup[:len(cleanup)-1]
	report["bulkCatalog"] = map[string]any{"applied": bulk.result, "inverse": bulkUndo.result}

	content := mustExecute(actionInput("magento.manage_content", "external", "pending_approval", "Acceptance CMS block versioned change", map[string]any{
		"operation": "cms_block_update", "scope": scope, "idempotency_key": marker + "-content",
		"rows": []any{map[string]any{"entity_ref": fmt.Sprintf("cms-block:%v", blockID), "path": map[string]any{"blockId": blockID},
			"body": map[string]any{"block": map[string]any{"content": blockContent + "\n<!-- " + marker + " -->"}}}},
	}), 3*time.Minute)
	if content.changesetID == "" {
		t.Fatal("Content action did not create a change-set")
	}
	cleanup = append(cleanup, content.changesetID)
	contentUndo := mustUndo(content.changesetID, "content")
	cleanup = cleanup[:len(cleanup)-1]
	report["content"] = map[string]any{"applied": content.result, "inverse": contentUndo.result}

	expectRejected(actionInput("magento.manage_promotions", "external", "pending_approval", "Acceptance promotion exposure rejection", map[string]any{
		"operation": "sales_rule_create", "scope": map[string]any{"website": "base"}, "projected_exposure": 1_000_000,
		"idempotency_key": marker + "-promo-cap",
		"rows": []any{map[string]any{"entity_ref": marker + "-promo", "body": map[string]any{"rule": map[string]any{
			"name": marker, "description": "Must be rejected before execution", "from_date": "2026-08-28", "to_date": "2026-08-30",
			"is_active": false, "discount_amount": 10}}}},
	}), `(?i)projected exposure`)
	report["promotionCap"] = "rejected_before_execution"

	expectRejected(actionInput("magento.manage_orders", "external", "pending_approval", "Acceptance prohibited-operation capability absence", map[string]any{
		"operation": "online_refund", "scope": map[string]any{"store": storeCode}, "idempotency_key": marker + "-class-zero",
		"rows": []any{map[string]any{"entity_ref": "order:acceptance", "body": map[string]any{"amount": 1}}},
	}), `(?i)not allowed|not executable|cannot be performed|magento admin`)
	report["handoffOnlyExecutePath"] = false

	handoff := mustExecute(actionInput("magento.financial_handoff", "internal", "approved", "Acceptance Magento Admin handoff", map[string]any{
		"handoff_kind": "online_refund", "entity_ref": "order:acceptance",
		"draft":    map[string]any{"amount": 1, "currency": "USD", "testOnly": true},
		"evidence": map[string]any{"reason": "local acceptance; no Magento execution"},
	}), 3*time.Minute)
	report["financialHandoff"] = handoff.result
	report["completedAt"] = time.Now().UTC().Format(time.RFC3339)
	report["ok"] = true
	out, _ := json.MarshalIndent(report, "", "  ")
	t.Log(string(out))
}
