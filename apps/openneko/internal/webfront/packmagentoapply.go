package webfront

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/packs"
)

var magentoReadTables = []string{
	"sales_order", "sales_order_item", "sales_order_address", "sales_order_status_history", "sales_order_payment",
	"sales_invoice", "sales_invoice_item", "sales_creditmemo", "sales_creditmemo_item", "sales_shipment",
	"sales_shipment_item", "customer_entity", "customer_address_entity", "eav_attribute", "catalog_product_entity",
	"catalog_product_entity_decimal", "catalog_product_entity_int", "catalog_product_entity_text",
	"catalog_product_entity_varchar", "catalog_category_product", "inventory_source_item", "inventory_reservation",
	"cataloginventory_stock_item", "store", "store_group", "store_website", "cron_schedule", "indexer_state",
}

var magentoSecretColumns = map[string][]string{
	"sales_order":     {"protect_code"},
	"customer_entity": {"password_hash", "rp_token", "rp_token_created_at", "confirmation"},
	"sales_order_payment": {"additional_data", "additional_information", "cc_number_enc", "cc_secure_verify",
		"cc_debug_request_body", "cc_debug_response_body", "cc_debug_response_serialized", "echeck_routing_number"},
}

var magentoHTTPClient = http.DefaultClient

var magentoDomainControls = []struct {
	domain    string
	riskClass int
	enabled   bool
}{
	{"catalog", 2, true}, {"inventory", 2, true}, {"orders", 2, true},
	{"promotions", 1, true}, {"content", 2, true}, {"customers", 1, false},
}

type magentoClassification struct {
	Domain     string `json:"domain"`
	EntityType string `json:"entityType"`
	Attribute  string `json:"attribute"`
	RiskClass  int    `json:"riskClass"`
	Category   string `json:"category"`
	Rationale  string `json:"rationale"`
}

// magentoPackClassifications is DEFAULT_MAGENTO_ATTRIBUTE_CLASSIFICATIONS of
// packages/packs; the table's check constraint accepts only its categories.
var magentoPackClassifications = func() []magentoClassification {
	var out []magentoClassification
	add := func(domain, entity string, risk int, rationale string, category func(string) string, attributes ...string) {
		for _, a := range attributes {
			out = append(out, magentoClassification{domain, entity, a, risk, category(a), rationale})
		}
	}
	fixed := func(c string) func(string) string { return func(string) string { return c } }
	copyText := func(a string) string {
		if strings.Contains(a, "description") || strings.HasPrefix(a, "meta_") {
			return "content"
		}
		return "operational"
	}
	add("catalog", "product", 2, "Reversible catalog metadata", copyText, "name", "sku", "attribute_set_id", "type_id", "status", "visibility",
		"weight", "description", "short_description", "meta_title", "meta_keyword", "meta_description", "url_key", "website_ids",
		"category_links", "product_links", "media_gallery_entries")
	add("catalog", "product", 2, "Price changes can run automatically only within the configured delta limit", fixed("financial"),
		"price", "special_price", "tier_price", "value")
	add("catalog", "product", 1, "Changes margin-sensitive cost data", fixed("financial"), "cost")
	add("catalog", "category", 2, "Reversible category configuration", copyText, "name", "is_active", "include_in_menu", "parent_id", "position",
		"available_sort_by", "default_sort_by", "description", "meta_title", "meta_keywords", "meta_description")
	add("inventory", "source_item", 2, "Reversible inventory operation with reservation-aware preview", fixed("operational"),
		"source_code", "sku", "quantity", "status", "notify_stock_qty", "use_config_notify_stock_qty")
	add("orders", "order", 2, "Routine order operations", fixed("operational"),
		"comment", "is_customer_notified", "is_visible_on_front", "hold_before_state", "hold_before_status")
	add("orders", "order", 1, "Customer address or identity data", fixed("pii"),
		"firstname", "lastname", "street", "city", "region", "postcode", "country_id", "telephone", "email")
	add("orders", "fulfillment", 1, "Customer-visible fulfillment state", fixed("operational"),
		"carrier_code", "title", "track_number", "items", "capture", "notify", "append_comment")
	add("promotions", "sales_rule", 1, "Promotion configuration changes financial exposure", func(a string) string {
		if a == "description" {
			return "content"
		}
		return "financial"
	}, "name", "description", "from_date", "to_date", "is_active", "uses_per_customer", "stop_rules_processing", "sort_order")
	add("promotions", "sales_rule", 1, "Promotion or coupon exposure", fixed("financial"),
		"discount_amount", "discount_qty", "discount_step", "times_used", "uses_per_coupon", "usage_limit", "qty")
	add("content", "cms", 2, "Versioned CMS content", fixed("content"),
		"identifier", "title", "content", "page_layout", "meta_title", "meta_keywords", "meta_description", "is_active", "store_id", "stores")
	add("customers", "customer", 1, "Customer personal data; domain is off by default", fixed("pii"),
		"firstname", "lastname", "email", "group_id", "dob", "taxvat", "gender", "addresses", "default_billing", "default_shipping")
	return out
}()

func magentoInputFrom(inputs *jsObj, secretValues map[string]string) magentoPreflightInput {
	return magentoPreflightInput{
		host:             jsValueString(inputs.vals["database.host"]),
		port:             jsToNumber(inputs.vals["database.port"]),
		database:         jsValueString(inputs.vals["database.name"]),
		username:         secretValues["database.analytics_username"],
		password:         secretValues["database.analytics_password"],
		tablePrefix:      jsValueString(nullishOr(inputs.vals["magento.table_prefix"], "")),
		baseURL:          jsValueString(inputs.vals["magento.base_url"]),
		storeCode:        jsValueString(nullishOr(inputs.vals["magento.store_code"], "all")),
		integrationToken: secretValues["magento.integration_token"],
		customersEnabled: packTruthy(inputs.vals["magento.customers_enabled"]),
	}
}

func jsNumberValue(v any) any {
	n := jsToNumber(v)
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return nil
	}
	return n
}

func magentoGraphjinTables(prefix string, available []string) []any {
	out := []any{}
	for _, name := range magentoReadTables {
		if !slices.Contains(available, name) {
			continue
		}
		table := map[string]any{"name": name, "table": prefix + name, "source": "magento_analytics", "database": "magento_analytics", "read_only": true}
		if blocked := magentoSecretColumns[name]; blocked != nil {
			table["blocklist"] = slices.Clone(blocked)
		}
		out = append(out, table)
	}
	return out
}

func magentoRelationships(bundle *packs.Bundle, available []string) []any {
	out := []any{}
	for _, art := range bundle.Artifacts {
		if art.Kind != packs.KindRelationships {
			continue
		}
		list, _ := art.Record()["relationships"].([]any)
		for _, item := range list {
			rel := jsObjMap(item)
			left, right := packString(rel["left"]), packString(rel["right"])
			if slices.Contains(available, strings.Split(left, ".")[0]) && slices.Contains(available, strings.Split(right, ".")[0]) {
				out = append(out, map[string]any{"from": "magento_analytics:" + left, "to": "magento_analytics:" + right})
			}
		}
		break
	}
	return out
}

func magentoV2OperationExposure(bundle *packs.Bundle) (map[string]any, error) {
	operations := map[string]any{}
	for _, art := range bundle.Artifacts {
		if art.Kind != packs.KindAction {
			continue
		}
		adapter, ok := art.Record()["adapter"].(map[string]any)
		if !ok || adapter["kind"] != "magento_changeset" && adapter["kind"] != "magento_governed_operation" {
			continue
		}
		declared, ok := adapter["operations"].(map[string]any)
		if !ok {
			continue
		}
		for _, key := range mapKeysOf(declared) {
			value, ok := declared[key].(map[string]any)
			if !ok {
				continue
			}
			operationID, root := "", ""
			if v, ok := value["operationId"]; ok && v != nil {
				operationID = packString(v)
			}
			if v, ok := value["mutationRoot"]; ok && v != nil {
				root = packString(v)
			}
			exposeAs := strings.TrimPrefix(root, "magento_operator_v2_")
			if operationID == "" || root == "" || exposeAs == root {
				name := operationID
				if name == "" {
					name = "<unknown>"
				}
				return nil, fmt.Errorf("Magento operation %s has an invalid V2 mutation root", name)
			}
			roles := []any{"magento_sensitive_executor"}
			if jsToNumber(value["defaultClass"]) == 2 {
				roles = []any{"magento_ops_executor", "magento_sensitive_executor"}
			}
			next := map[string]any{"expose_mutation": true, "allowed_roles": roles, "expose_as": exposeAs}
			if current, ok := operations[operationID]; ok {
				a, err1 := packs.CanonicalHash(current)
				b, err2 := packs.CanonicalHash(next)
				if err1 != nil || err2 != nil || a != b {
					return nil, fmt.Errorf("Magento operation %s has conflicting V2 exposure", operationID)
				}
			}
			operations[operationID] = next
		}
	}
	return operations, nil
}

func mapKeysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func magentoGraphjinUpdate(inputs map[string]any, secretValues map[string]string, preflight *magentoPreflightResult,
	bundle *packs.Bundle, retired []string) (map[string]any, error) {
	exposure, err := magentoV2OperationExposure(bundle)
	if err != nil {
		return nil, err
	}
	token := secretValues["magento.integration_token"]
	write := token != ""
	spec := func(operations map[string]any) map[string]any {
		out := map[string]any{"base_url": packString(inputs["magento.base_url"]), "operations": operations}
		if write {
			out["auth"] = map[string]any{"scheme": "bearer", "token": token}
		}
		return out
	}
	access := func(allowed bool) string {
		if allowed {
			return "authenticated"
		}
		return "blocked"
	}
	analytics := map[string]any{
		"name": "magento_analytics", "kind": "database", "default": false, "type": preflight.databaseType,
		"host": packString(inputs["database.host"]), "port": jsNumberValue(inputs["database.port"]),
		"dbname": packString(inputs["database.name"]), "read_only": true, "analytics_mode": true,
		"capabilities": map[string]any{"data.read": true, "data.write": false, "schema.read": true, "schema.write": false},
		"access":       map[string]any{"read": "authenticated", "write": "blocked", "delete": "blocked", "blocked_tables": slices.Clone(preflight.blockedTables)},
	}
	if v, ok := secretValues["database.analytics_username"]; ok {
		analytics["user"] = v
	}
	if v, ok := secretValues["database.analytics_password"]; ok {
		analytics["password"] = v
	}
	update := map[string]any{
		"roles": []any{
			map[string]any{"name": "magento_ops_executor", "comment": "Short-lived Magento executor for approved automations"},
			map[string]any{"name": "magento_sensitive_executor", "comment": "Short-lived Magento executor minted after administrator approval"},
		},
		"update_sources": []any{analytics, map[string]any{
			"name": "magento_operator", "kind": "api", "default": false, "specs_dir": "/config/specs",
			"specs": map[string]any{
				"magento-operator-v1": spec(map[string]any{"magentoAddInternalOrderComment": map[string]any{"expose_mutation": false, "allowed_roles": []any{}}}),
				"magento-operator-v2": spec(exposure),
			},
			"read_only":    !write,
			"capabilities": map[string]any{"api.read": true, "api.write": write, "api.delete": write},
			"access":       map[string]any{"read": "authenticated", "write": access(write), "delete": access(write)},
		}},
		"tables":        magentoGraphjinTables(preflight.tablePrefix, preflight.availableTables),
		"relationships": magentoRelationships(bundle, preflight.availableTables),
	}
	if len(retired) > 0 {
		patches := []any{}
		for _, name := range retired {
			patches = append(patches, map[string]any{"name": name, "read_only": true,
				"access": map[string]any{"read": "blocked", "write": "blocked", "delete": "blocked"}})
		}
		update["source_patches"] = patches
	}
	return update, nil
}

func magentoCapsFromInputs(inputs map[string]any) map[string]any {
	return map[string]any{
		"maxRowsPerChangeset":  jsNumberValue(inputs["magento.max_rows_per_changeset"]),
		"maxPriceDeltaPercent": jsNumberValue(inputs["magento.max_price_delta_percent"]),
		"maxDiscountPercent":   jsNumberValue(inputs["magento.max_discount_percent"]),
		"maxCouponCount":       jsNumberValue(inputs["magento.max_coupon_count"]),
		"maxProjectedExposure": jsNumberValue(inputs["magento.max_projected_exposure"]),
		"maxDailyAutoActions":  jsNumberValue(inputs["magento.max_daily_auto_actions"]),
		"maxStoreCredit":       50,
		"minPromotionDays":     1,
		"skuCooldownSeconds":   jsNumberValue(inputs["magento.sku_cooldown_seconds"]),
	}
}

func (p *packApplyRun) writeMagentoControls(ctx context.Context, tx pgx.Tx, inputs map[string]any, preflight *magentoPreflightResult) error {
	caps := packJSON(magentoCapsFromInputs(inputs))
	scope := packJSON(map[string]any{"stores": preflight.scopes})
	for _, control := range magentoDomainControls {
		enabled := control.enabled
		if control.domain == "customers" {
			enabled = packTruthy(inputs["magento.customers_enabled"])
		}
		if _, err := tx.Exec(ctx, `insert into magento_store_control (org_id, domain, risk_class, enabled, auto_execute, caps, scope)
			values ($1, $2, $3, $4, false, $5::jsonb, $6::jsonb)
			on conflict (org_id, domain) do update set enabled = excluded.enabled, caps = excluded.caps, scope = excluded.scope, updated_at = now()`,
			p.orgID, control.domain, control.riskClass, enabled, caps, scope); err != nil {
			return err
		}
	}
	for _, c := range magentoPackClassifications {
		if _, err := tx.Exec(ctx, `insert into magento_attribute_classification (org_id, domain, entity_type, attribute, risk_class, category,
			rationale, reviewed, pack_default)
			values ($1, $2, $3, $4, $5, $6, $7, true, true)
			on conflict (org_id, domain, entity_type, attribute) do update set risk_class = excluded.risk_class, category = excluded.category,
			rationale = excluded.rationale, reviewed = true, pack_default = true, updated_at = now()`,
			p.orgID, c.Domain, c.EntityType, c.Attribute, c.RiskClass, c.Category, c.Rationale); err != nil {
			return err
		}
	}
	return nil
}

func magentoMetricRuntime(inputs map[string]any, preflight *magentoPreflightResult) map[string]any {
	return map[string]any{
		"storeIds": preflight.storeIDs, "windowDays": 30, "staleAfterSeconds": 2 * 60 * 60, "agedAfterSeconds": 2 * 24 * 60 * 60,
		"stockThreshold": 5, "currency": packString(nullishOr(inputs["magento.base_currency"], "USD")),
		"timezone": packString(nullishOr(inputs["magento.timezone"], "UTC")),
	}
}

func magentoWatcherVariables(preflight *magentoPreflightResult) map[string]any {
	return map[string]any{
		"from":        map[string]any{"kind": "seconds_ago", "seconds": 30 * 24 * 60 * 60},
		"to":          map[string]any{"kind": "now"},
		"staleBefore": map[string]any{"kind": "seconds_ago", "seconds": 2 * 60 * 60},
		"olderThan":   map[string]any{"kind": "seconds_ago", "seconds": 2 * 24 * 60 * 60},
		"storeIds":    map[string]any{"kind": "literal", "value": preflight.storeIDs},
		"threshold":   map[string]any{"kind": "literal", "value": 5},
	}
}

func magentoActionReason(value map[string]any, preflight *magentoPreflightResult) string {
	adapter, _ := value["adapter"].(map[string]any)
	if k := adapter["kind"]; k == "graphjin_api_operation" || k == "magento_financial_handoff" {
		return "ready"
	}
	if preflight == nil {
		return "unsupported_adapter"
	}
	readiness, _ := value["readiness"].(map[string]any)
	if reason, ok := preflight.operatorDomains[packStringOr(readiness["domain"])]; ok {
		return reason
	}
	return preflight.operatorReadiness
}

func packStringOr(v any) string {
	if v == nil {
		return ""
	}
	return packString(v)
}

// magentoArtifactReadiness keeps Node's text, "undefined" included, for a
// domain that the preflight did not probe.
func magentoArtifactReadiness(art packs.Artifact, preflight *magentoPreflightResult) (string, any) {
	operator := art.Key == "source.magento_operator" || art.Kind == packs.KindSpec || art.Kind == packs.KindAction ||
		art.Kind == packs.KindSavedQuery && strings.HasPrefix(path.Base(art.Path), "action_")
	if !operator {
		return "ready", nil
	}
	domain, reason := "", ""
	var adapter map[string]any
	if art.Kind == packs.KindAction {
		value := art.Record()
		readiness, _ := value["readiness"].(map[string]any)
		domain = packStringOr(readiness["domain"])
		adapter, _ = value["adapter"].(map[string]any)
	}
	switch {
	case adapter["kind"] == "magento_financial_handoff":
		reason = "ready"
	case domain != "":
		var ok bool
		if reason, ok = preflight.operatorDomains[domain]; !ok {
			reason = "undefined"
		}
	default:
		reason = preflight.operatorReadiness
	}
	readiness := "ready"
	if reason != "ready" {
		readiness = "blocked"
	}
	return readiness, "operator:" + cmpOr(domain, "operator") + ":" + reason
}
