package webfront

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

func TestMagentoRuntimeParityFixture(t *testing.T) {
	raw, err := os.ReadFile("../packs/testdata/magento_runtime_parity.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jsParse(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range jsGet(parsed, "reconciliation").([]any) {
		var op magentoOperation
		if err := json.Unmarshal([]byte(jsStringify(jsGet(item, "operation"), false)), &op); err != nil {
			t.Fatal(err)
		}
		got := magentoReconciliationConfirmed(&op, asJSObj(jsGet(item, "reconciled")), jsGet(item, "expected"))
		if got != jsGet(item, "confirmed") {
			t.Errorf("reconciliation %q: got %v", jsGet(item, "name"), got)
		}
	}
	for _, item := range jsGet(parsed, "mutationField").([]any) {
		field, err := magentoGraphjinMutationField(jsGet(item, "root").(string))
		if want, ok := jsGet(item, "error").(string); ok {
			if err == nil || err.Error() != want {
				t.Errorf("mutation field %v: got %q, %v", jsGet(item, "root"), field, err)
			}
		} else if err != nil || field != jsGet(item, "field") {
			t.Errorf("mutation field %v: got %q, %v", jsGet(item, "root"), field, err)
		}
	}
	for _, item := range jsGet(parsed, "terminalBulk").([]any) {
		if got := isTerminalMagentoBulkStatus(jsGet(item, "status").(float64)); got != jsGet(item, "terminal") {
			t.Errorf("bulk status %v: got %v", jsGet(item, "status"), got)
		}
	}
}

func TestMagentoReadBoundaries(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var lastURL string
	reply := `{"items":[{"sku":"sku-a","source_code":"warehouse","quantity":21,"status":1}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastURL = r.URL.String()
		body := reply
		mu.Unlock()
		if body == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	rt := &magentoRuntime{baseURL: srv.URL, storeCode: "default", token: "t"}
	inventory := &magentoOperation{OperationID: "magentoSaveSourceItems", ReadPath: "/V1/inventory/source-items", BodyKey: "sourceItems", EntityType: "source_item"}
	body, _ := jsParse([]byte(`{"sourceItems":[{"sku":"sku-a","source_code":"warehouse","quantity":21,"status":1}]}`))
	legacy := jsObjOf("searchCriteria[filter_groups][0][filters][0][value]", "wrong-sku")

	got, err := readMagentoEntity(ctx, rt, inventory, emptyJSObj(), legacy, body)
	if err != nil || jsGet(got, "quantity") != float64(21) {
		t.Fatalf("source item read: %v %v", jsStringify(got, false), err)
	}
	if strings.Contains(lastURL, "wrong-sku") || !strings.Contains(lastURL, "%5Bvalue%5D=sku-a") || !strings.Contains(lastURL, "%5Bvalue%5D=warehouse") {
		t.Fatalf("source item filters: %s", lastURL)
	}
	for _, bad := range []string{
		`{"items":[{"sku":"sku-a","source_code":"warehouse"},{"sku":"sku-a","source_code":"warehouse"}]}`,
		`{"items":[{"sku":"sku-a","source_code":"default"}]}`,
		`{}`,
	} {
		mu.Lock()
		reply = bad
		mu.Unlock()
		if _, err := readMagentoEntity(ctx, rt, inventory, emptyJSObj(), emptyJSObj(), body); err == nil || !strings.Contains(err.Error(), "Magento source-item read") {
			t.Fatalf("source item reply %s: %v", bad, err)
		}
	}
	mu.Lock()
	reply = `{"items":[]}`
	mu.Unlock()
	if got, err := readMagentoEntity(ctx, rt, inventory, emptyJSObj(), emptyJSObj(), body); got != nil || err != nil {
		t.Fatalf("empty source item collection: %v %v", got, err)
	}
	two, _ := jsParse([]byte(`{"sourceItems":[{"sku":"a","source_code":"w"},{"sku":"b","source_code":"w"}]}`))
	if _, err := readMagentoEntity(ctx, rt, inventory, emptyJSObj(), emptyJSObj(), two); err == nil || !strings.Contains(err.Error(), "exactly one source item") {
		t.Fatalf("two source items: %v", err)
	}

	order := &magentoOperation{OperationID: "magentoUpdateOrder", ReadPath: "/V1/orders/{id}", BodyKey: "entity", EntityType: "order", NumericPathParams: []string{"id"}}
	for _, entity := range []string{`{}`, `{"entity_id":64}`, `{"entity_id":0}`, `{"entity_id":"63"}`} {
		b, _ := jsParse([]byte(`{"entity":` + entity + `}`))
		if _, err := readMagentoEntity(ctx, rt, order, jsObjOf("id", float64(63)), emptyJSObj(), b); err == nil || !strings.Contains(err.Error(), "entity.entity_id to match") {
			t.Fatalf("order identity %s: %v", entity, err)
		}
	}
	mu.Lock()
	reply = ""
	mu.Unlock()
	b, _ := jsParse([]byte(`{"entity":{"entity_id":63}}`))
	if _, err := readMagentoEntity(ctx, rt, order, jsObjOf("id", "63"), emptyJSObj(), b); err == nil || err.Error() != "Magento order update requires an existing order" {
		t.Fatalf("absent order: %v", err)
	}
	if _, err := readMagentoEntity(ctx, rt, &magentoOperation{OperationID: "x", ReadPath: "/V1/products/{sku}"}, jsObjOf("sku", " "), emptyJSObj(), nil); err == nil || err.Error() != "Magento read path is missing sku" {
		t.Fatalf("missing path value: %v", err)
	}
	path, err := magentoNormalizePath(order, jsObjOf("id", "63"), "default")
	if err != nil || jsStringify(path, false) != `{"storeCode":"default","id":63}` {
		t.Fatalf("normalized path: %s %v", jsStringify(path, false), err)
	}
	if _, err := magentoNormalizePath(order, jsObjOf("id", "0"), "default"); err == nil {
		t.Fatal("a zero order id passed")
	}
}

type fakeMagento struct {
	mu       sync.Mutex
	products map[string]map[string]any
	calls    []map[string]any
	roles    []string
	bulk     string
}

func (m *fakeMagento) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost && r.URL.Path == "/api/v1/graphql" {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(raw, &req)
		if strings.Contains(req.Query, "MagentoIndexerReceipt") {
			_, _ = w.Write([]byte(`{"data":{"indexer_state":[{"indexer_id":"catalog_product_price","status":"valid","updated":"2026-11-14"}]}}`))
			return
		}
		call, _ := req.Variables["call"].(map[string]any)
		m.calls = append(m.calls, call)
		m.roles = append(m.roles, r.Header.Get("X-Role"))
		apply := func(sku string, product map[string]any) {
			current := m.products[sku]
			for k, v := range product {
				current[k] = v
			}
		}
		field := "magento_update_product"
		response := `"{\"sku\":\"SKU-1\"}"`
		if strings.Contains(req.Query, "magento_bulk_update_products") {
			field = "magento_bulk_update_products"
			for _, item := range call["body"].([]any) {
				product := item.(map[string]any)["product"].(map[string]any)
				apply(product["sku"].(string), product)
			}
			response = `"{\"bulk_uuid\":\"bulk-0001-uuid\"}"`
		} else {
			body, _ := call["body"].(map[string]any)
			product, _ := body["product"].(map[string]any)
			apply(call["path"].(map[string]any)["sku"].(string), product)
		}
		_, _ = w.Write([]byte(`{"data":{"` + field + `":{"ok":true,"status_code":200,"operation_id":"op","request_id":"req-1","response_json":` + response + `}}}`))
		return
	}
	if strings.HasPrefix(r.URL.Path, "/rest/default/V1/bulk/") {
		_, _ = w.Write([]byte(m.bulk))
		return
	}
	if sku, ok := strings.CutPrefix(r.URL.Path, "/rest/default/V1/products/"); ok {
		product, found := m.products[sku]
		if !found {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, _ := json.Marshal(product)
		_, _ = w.Write(raw)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func TestMagentoChangesetRuntime(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	ctx := context.Background()
	seedRiver(t, db, f)
	saved := magentoBulkPoll
	magentoBulkPoll = 10 * time.Millisecond
	defer func() { magentoBulkPoll = saved }()

	shop := &fakeMagento{
		products: map[string]map[string]any{
			"SKU-1": {"sku": "SKU-1", "name": "Old", "price": 10.0},
			"SKU-2": {"sku": "SKU-2", "name": "Two", "price": 20.0},
		},
		bulk: `{"operations_list":[{"id":1,"status":1}]}`,
	}
	srv := httptest.NewServer(shop)
	defer srv.Close()
	graphqlURL := srv.URL + "/api/v1/graphql"
	const sourceID = "52222222-2222-4222-8222-222222222222"
	seed(t, db, `insert into data_source (id, org_id, kind, name, graphql_url, mcp_url, auth_mode, is_default, enabled)
		values ($1, 'org', 'graphjin', 'magento', $2, $3, 'jwt', true, true)`, sourceID, graphqlURL, srv.URL+"/api/v1/mcp")
	cfg, _ := json.Marshal(map[string]any{
		"magento.base_url":   srv.URL + "/",
		"magento.store_code": "default",
		"_runtime":           map[string]any{"source": map[string]any{"id": sourceID, "graphqlUrl": graphqlURL, "authMode": "jwt"}},
	})
	const installID = "51111111-1111-4111-8111-111111111111"
	seed(t, db, `insert into pack_install (id, org_id, pack_id, version, status, manifest_hash, config, installed_at)
		values ($2, 'org', 'magento', '2.0.0', 'installed', 'h', $1::jsonb, now())`, string(cfg), installID)
	if err := secrets.Write(secrets.Store{"pack.magento": {"MAGENTO_INTEGRATION_TOKEN": "magento-secret-token"}}, a.SecretsDir); err != nil {
		t.Fatal(err)
	}
	catalog := `{"kind":"magento.manage_catalog","domain":"catalog","adapter":{"kind":"magento_changeset","operations":{
		"product_update":{"operationId":"magentoUpdateProduct","mutationRoot":"magento_operator_v2_magento_update_product","readPath":"/V1/products/{sku}","defaultClass":2,"entityType":"product","reversible":true,"bodyKey":"product","inverseOperation":"product_update","entityIdField":"sku"},
		"product_bulk_update":{"operationId":"magentoBulkUpdateProducts","mutationRoot":"magento_operator_v2_magento_bulk_update_products","readPath":"/V1/products/{sku}","defaultClass":2,"entityType":"product","reversible":true,"bodyKey":"product","inverseOperation":"product_bulk_update","entityIdField":"sku","resultMode":"async_bulk"}}}}`
	for kind, def := range map[string]string{
		"magento.manage_catalog":    catalog,
		"magento.undo_changeset":    `{"kind":"magento.undo_changeset","domain":"catalog","adapter":{"kind":"magento_changeset","operations":{}}}`,
		"magento.financial_handoff": `{"kind":"magento.financial_handoff","domain":"orders","adapter":{"kind":"magento_financial_handoff"}}`,
	} {
		seed(t, db, `insert into pack_action_definition (org_id, kind, description, definition, definition_hash, readiness, enabled)
			values ('org', $1, $1, $2::jsonb, 'h', 'ready', true)`, kind, def)
		seed(t, db, `insert into pack_artifact (pack_install_id, org_id, artifact_kind, artifact_key, target_ref, desired_hash, last_applied_hash, ownership, readiness)
			values ($1, 'org', 'action', $2, $2, 'h', 'h', 'managed', 'ready')`, installID, kind)
	}
	seed(t, db, `insert into magento_store_control (org_id, domain, risk_class, enabled, auto_execute, caps) values ('org', 'catalog', 2, true, true, '{}')`)

	create := func(kind, status, payload string) (string, string, error) {
		in, _ := jsParse([]byte(`{"orgId":"org","scope":"external","kind":"` + kind + `","status":"` + status +
			`","summary":"Rename product","actorUserId":"member","actorRole":"member","actorBackend":"ax","payload":` + payload + `}`))
		out, err := a.createActionRequestInGo(ctx, in.(*jsObj))
		if err != nil {
			return "", "", err
		}
		return out.vals["id"].(string), out.vals["status"].(string), nil
	}
	approve := func(id string) {
		seed(t, db, `update action_request set status = 'approved', approved_by_user_id = 'admin', approved_at = now() where id = $1::uuid`, id)
	}
	text := func(q string, args ...any) string { t.Helper(); return rowText(t, db, q, args...) }

	reqID, status, err := create("magento.manage_catalog", "pending_approval",
		`{"operation":"product_update","scope":{"store":"default"},"idempotency_key":"rename-sku-1","rows":[{"entity_ref":"SKU-1","path":{"sku":"SKU-1"},"body":{"product":{"name":"New"}}}]}`)
	if err != nil || status != "pending_approval" {
		t.Fatalf("create change: %s %v", status, err)
	}
	changeset := text(`select payload->>'changeset_id' from action_request where id = $1::uuid`, reqID)
	if got := text(`select payload->'preview'->>'execution_mode' || '|' || (payload->'preview'->'rows'->0->'before')::text from action_request where id = $1::uuid`, reqID); got !=
		`controlled_automation_eligible|{"name": "Old"}` {
		t.Fatalf("preview: %s", got)
	}
	if got := text(`select c.status || '|' || c.risk_class || '|' || r.status || '|' || (r.before_image->>'name') from action_changeset c join action_changeset_row r on r.changeset_id = c.id where c.id = $1::uuid`, changeset); got != "pending_approval|2|draft|Old" {
		t.Fatalf("stored change-set: %s", got)
	}
	if got := text(`select string_agg(event, ',' order by seq) from audit_chain where org_id = 'org' and entity_kind = 'action_changeset'`); got != "previewed" {
		t.Fatalf("preview audit: %s", got)
	}

	approve(reqID)
	if err := a.ActionExecute(ctx, "org", reqID); err != nil {
		t.Fatal(err)
	}
	if got := text(`select r.status || '|' || e.status || '|' || e.changeset_id::text || '|' || e.command_or_operation || '|' || (e.result->>'applied') || '|' || (e.result->'indexers'->0->>'indexer_id')
		from action_request r join action_execution e on e.action_request_id = r.id where r.id = $1::uuid`, reqID); got != "executed|succeeded|"+changeset+"|product_update|1|catalog_product_price" {
		t.Fatalf("execution: %s", got)
	}
	if got := text(`select c.status || '|' || c.approved_by_user_id || '|' || r.status || '|' || r.external_ref || '|' || (r.reconciled_image->>'name') from action_changeset c join action_changeset_row r on r.changeset_id = c.id where c.id = $1::uuid`, changeset); got != "applied|admin|applied|req-1|New" {
		t.Fatalf("applied change-set: %s", got)
	}
	shop.mu.Lock()
	call, _ := json.Marshal(shop.calls[0])
	role := shop.roles[0]
	shop.mu.Unlock()
	if string(call) != `{"body":{"product":{"name":"New"}},"path":{"sku":"SKU-1","storeCode":"default"}}` || role != "magento_ops_executor" {
		t.Fatalf("mutation call: %s %s", call, role)
	}

	undoID, _, err := create("magento.undo_changeset", "pending_approval", `{"changeset_id":"`+changeset+`","idempotency_key":"undo-rename-sku-1"}`)
	if err != nil {
		t.Fatalf("create undo: %v", err)
	}
	inverse := text(`select payload->>'changeset_id' from action_request where id = $1::uuid`, undoID)
	if got := text(`select c.inverse_of_id::text || '|' || c.summary || '|' || r.after_image::text from action_changeset c join action_changeset_row r on r.changeset_id = c.id where c.id = $1::uuid`, inverse); got !=
		changeset+`|Undo: Rename product|{"product": {"name": "Old"}}` {
		t.Fatalf("inverse draft: %s", got)
	}
	shop.mu.Lock()
	shop.products["SKU-1"]["name"] = "Edited in Magento"
	shop.mu.Unlock()
	approve(undoID)
	if err := a.ActionExecute(ctx, "org", undoID); err != nil {
		t.Fatal(err)
	}
	if got := text(`select r.status || '|' || e.status || '|' || c.status || '|' || cr.status from action_request r
		join action_execution e on e.action_request_id = r.id
		join action_changeset c on c.id = (r.payload->>'changeset_id')::uuid
		join action_changeset_row cr on cr.changeset_id = c.id where r.id = $1::uuid`, undoID); got != "executed|reconcile_required|reconcile_required|drifted" {
		t.Fatalf("drifted undo: %s", got)
	}
	shop.mu.Lock()
	writes := len(shop.calls)
	shop.mu.Unlock()
	if writes != 1 {
		t.Fatalf("a drifted row reached Magento: %d writes", writes)
	}

	if _, _, err := create("magento.manage_catalog", "approved",
		`{"operation":"product_update","scope":{"store":"default"},"idempotency_key":"price-sku-1","rows":[{"entity_ref":"SKU-1","path":{"sku":"SKU-1"},"body":{"product":{"price":10.5}}}]}`); err == nil ||
		err.Error() != "This Magento change requires approval from a human administrator" {
		t.Fatalf("price change without a person: %v", err)
	}

	seed(t, db, `insert into magento_auto_rule (id, org_id, name, instruction, domain, action_kind, daily_cap, enabled)
		values ('62222222-2222-4222-8222-222222222222', 'org', 'rename', 'rename', 'catalog', 'magento.manage_catalog', 1, true)`)
	autoID, autoStatus, err := create("magento.manage_catalog", "pending_approval",
		`{"operation":"product_update","scope":{"store":"default"},"idempotency_key":"auto-sku-2","auto_rule_id":"62222222-2222-4222-8222-222222222222","rows":[{"entity_ref":"SKU-2","path":{"sku":"SKU-2"},"body":{"product":{"name":"Two B"}}}]}`)
	if err != nil || autoStatus != "approved" {
		t.Fatalf("automatic change: %s %v", autoStatus, err)
	}
	if err := a.ActionExecute(ctx, "org", autoID); err != nil {
		t.Fatal(err)
	}
	if got := text(`select e.status || '|' || c.status || '|' || cr.external_ref || '|' || cr.status || '|' || (c.cap_snapshot->>'autoRuleId') from action_request r
		join action_execution e on e.action_request_id = r.id
		join action_changeset c on c.id = (r.payload->>'changeset_id')::uuid
		join action_changeset_row cr on cr.changeset_id = c.id where r.id = $1::uuid`, autoID); got != "succeeded|applied|req-1|applied|62222222-2222-4222-8222-222222222222" {
		t.Fatalf("automatic change: %s", got)
	}
	if got := text(`select (last_fired_at is not null)::text from magento_auto_rule where name = 'rename'`); got != "true" {
		t.Fatalf("rule last fired: %s", got)
	}
	if _, _, err := create("magento.manage_catalog", "pending_approval",
		`{"operation":"product_update","scope":{"store":"default"},"idempotency_key":"auto-sku-2b","auto_rule_id":"62222222-2222-4222-8222-222222222222","rows":[{"entity_ref":"SKU-2","path":{"sku":"SKU-2"},"body":{"product":{"name":"Two C"}}}]}`); err == nil ||
		err.Error() != "Magento automatic rule daily cap of 1 is exhausted" {
		t.Fatalf("daily cap: %v", err)
	}
	if got := text(`select enabled::text || '|' || suspended_reason from magento_auto_rule where name = 'rename'`); got != "false|daily_cap_exhausted" {
		t.Fatalf("rule suspended: %s", got)
	}

	bulkID, bulkStatus, err := create("magento.manage_catalog", "pending_approval",
		`{"operation":"product_bulk_update","scope":{"store":"default"},"idempotency_key":"bulk-sku-2","rows":[{"entity_ref":"SKU-2","path":{"sku":"SKU-2"},"body":{"product":{"sku":"SKU-2","name":"Two D"}}}]}`)
	if err != nil || bulkStatus != "pending_approval" {
		t.Fatalf("bulk change: %s %v", bulkStatus, err)
	}
	approve(bulkID)
	if err := a.ActionExecute(ctx, "org", bulkID); err != nil {
		t.Fatal(err)
	}
	if got := text(`select e.status || '|' || c.status || '|' || c.risk_class || '|' || c.bulk_uuid || '|' || cr.status || '|' || (e.result->>'bulk_status_recorded') from action_request r
		join action_execution e on e.action_request_id = r.id
		join action_changeset c on c.id = (r.payload->>'changeset_id')::uuid
		join action_changeset_row cr on cr.changeset_id = c.id where r.id = $1::uuid`, bulkID); got != "succeeded|applied|1|bulk-0001-uuid|applied|true" {
		t.Fatalf("bulk execution: %s", got)
	}
	shop.mu.Lock()
	bulkCall, _ := json.Marshal(shop.calls[len(shop.calls)-1])
	bulkRole := shop.roles[len(shop.roles)-1]
	shop.mu.Unlock()
	if string(bulkCall) != `{"body":[{"product":{"name":"Two D","sku":"SKU-2"}}],"path":{"storeCode":"default"}}` || bulkRole != "magento_sensitive_executor" {
		t.Fatalf("bulk call: %s %s", bulkCall, bulkRole)
	}

	handoffID, _, err := create("magento.financial_handoff", "pending_approval",
		`{"handoff_kind":"online_refund","entity_ref":"order:63","draft":{"amount":12.5},"evidence":{"reason":"damaged"}}`)
	if err != nil {
		t.Fatal(err)
	}
	approve(handoffID)
	if err := a.ActionExecute(ctx, "org", handoffID); err != nil {
		t.Fatal(err)
	}
	if got := text(`select h.status || '|' || h.kind || '|' || e.command_or_operation || '|' || (e.result->>'execute_path') || '|' || r.status from magento_financial_handoff h
		join action_execution e on e.external_ref = h.id::text join action_request r on r.id = h.action_request_id where r.id = $1::uuid`, handoffID); got != "ready_for_human|online_refund|magento_admin_handoff|false|executed" {
		t.Fatalf("handoff: %s", got)
	}

	if got := text(`select count(*)::text from audit_chain where org_id = 'org' and payload::text like '%magento-secret-token%'`); got != "0" {
		t.Fatalf("the integration token reached the audit chain")
	}
}
