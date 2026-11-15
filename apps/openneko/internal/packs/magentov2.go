package packs

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type MagentoDomain string

const (
	MagentoCatalog    MagentoDomain = "catalog"
	MagentoInventory  MagentoDomain = "inventory"
	MagentoOrders     MagentoDomain = "orders"
	MagentoPromotions MagentoDomain = "promotions"
	MagentoContent    MagentoDomain = "content"
	MagentoCustomers  MagentoDomain = "customers"
)

var MagentoDomains = []MagentoDomain{
	MagentoCatalog,
	MagentoInventory,
	MagentoOrders,
	MagentoPromotions,
	MagentoContent,
	MagentoCustomers,
}

type MagentoRiskClass int

type MagentoExecutionMode string

const (
	MagentoApprovalRequired     MagentoExecutionMode = "approval_required"
	MagentoControlledAutomation MagentoExecutionMode = "controlled_automation_eligible"
	MagentoHandoffOnly          MagentoExecutionMode = "handoff_only"
)

type MagentoAttributeCategory string

const (
	MagentoCatFinancial   MagentoAttributeCategory = "financial"
	MagentoCatPII         MagentoAttributeCategory = "pii"
	MagentoCatContent     MagentoAttributeCategory = "content"
	MagentoCatOperational MagentoAttributeCategory = "operational"
)

type MagentoAttributeClassification struct {
	Domain     MagentoDomain            `json:"domain"`
	EntityType string                   `json:"entityType"`
	Attribute  string                   `json:"attribute"`
	RiskClass  MagentoRiskClass         `json:"riskClass"`
	Category   MagentoAttributeCategory `json:"category"`
	Rationale  string                   `json:"rationale"`
}

func MagentoExecutionModeFor(riskClass MagentoRiskClass) MagentoExecutionMode {
	if riskClass == 1 {
		return MagentoApprovalRequired
	}
	if riskClass == 2 {
		return MagentoControlledAutomation
	}
	return MagentoHandoffOnly
}

var MagentoHandoffOnlyOperations = []string{
	"magentoIssueOnlineCreditmemo",
	"magentoApproveReturn",
	"magentoUpdatePaymentConfiguration",
	"magentoUpdateTaxConfiguration",
	"magentoUpdateCurrencyConfiguration",
	"magentoIssueStoreCreditOverCap",
}

func IsMagentoHandoffOnlyOperation(operationID string) bool {
	return slices.Contains(MagentoHandoffOnlyOperations, operationID)
}

// The table's check constraint accepts only these categories.
var DefaultMagentoAttributeClassifications = func() []MagentoAttributeClassification {
	var out []MagentoAttributeClassification
	add := func(domain MagentoDomain, entity string, risk MagentoRiskClass, rationale string, category func(string) MagentoAttributeCategory, attributes ...string) {
		for _, a := range attributes {
			out = append(out, MagentoAttributeClassification{domain, entity, a, risk, category(a), rationale})
		}
	}
	fixed := func(c MagentoAttributeCategory) func(string) MagentoAttributeCategory {
		return func(string) MagentoAttributeCategory { return c }
	}
	copyText := func(a string) MagentoAttributeCategory {
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
	add("promotions", "sales_rule", 1, "Promotion configuration changes financial exposure", func(a string) MagentoAttributeCategory {
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

type MagentoDomainCaps struct {
	MaxRowsPerChangeset float64 `json:"maxRowsPerChangeset"`
	MaxPriceDeltaPct    float64 `json:"maxPriceDeltaPercent"`
	MaxDiscountPct      float64 `json:"maxDiscountPercent"`
	MaxCouponCount      float64 `json:"maxCouponCount"`
	MaxProjectedExp     float64 `json:"maxProjectedExposure"`
	MaxDailyAutoActions float64 `json:"maxDailyAutoActions"`
	MaxStoreCredit      float64 `json:"maxStoreCredit"`
	MinPromotionDays    float64 `json:"minPromotionDays"`
	SkuCooldownSeconds  float64 `json:"skuCooldownSeconds"`
}

var DefaultMagentoCaps = map[MagentoDomain]MagentoDomainCaps{
	MagentoCatalog:    {50, 10, 0, 0, 5000, 10, 0, 0, 3600},
	MagentoInventory:  {100, 0, 0, 0, 10000, 20, 0, 0, 1800},
	MagentoOrders:     {10, 0, 0, 0, 2500, 20, 0, 0, 300},
	MagentoPromotions: {10, 0, 25, 100, 5000, 0, 0, 1, 3600},
	MagentoContent:    {10, 0, 0, 0, 0, 5, 0, 0, 1800},
	MagentoCustomers:  {10, 0, 0, 0, 500, 0, 50, 0, 3600},
}

var wrapperKeys = map[string]bool{
	"product": true, "category": true, "block": true, "page": true,
	"rule": true, "customer": true, "address": true, "entity": true,
	"sourceItem": true, "source_item": true, "stockItem": true, "stock_item": true,
	"statusHistory": true, "status_history": true, "extension_attributes": true,
}

func collectModifiedAttributes(value any, out map[string]any, order *[]string) {
	add := func(k string, v any) {
		if _, exists := out[k]; !exists {
			*order = append(*order, k)
		}
		out[k] = v
	}
	if list, ok := value.([]any); ok {
		for _, item := range list {
			collectModifiedAttributes(item, out, order)
		}
		return
	}
	rec, ok := asStringMap(value)
	if !ok {
		return
	}
	for _, key := range mapKeys(value) {
		entry := rec[key]
		if key == "custom_attributes" {
			if attrs, ok := entry.([]any); ok {
				for _, attr := range attrs {
					if m, ok := asStringMap(attr); ok {
						if code, ok := m["attribute_code"].(string); ok && strings.TrimSpace(code) != "" {
							add(strings.TrimSpace(code), m["value"])
						}
					}
				}
				continue
			}
		}
		if wrapperKeys[key] {
			collectModifiedAttributes(entry, out, order)
			continue
		}
		if (key == "items" || key == "sourceItems") && isSlice(entry) {
			collectModifiedAttributes(entry, out, order)
			continue
		}
		add(key, entry)
	}
}

func asStringMap(v any) (map[string]any, bool) {
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	return nil, false
}

func mapKeys(v any) []string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, utf16Less)
	return keys
}

func isSlice(v any) bool {
	_, ok := v.([]any)
	return ok
}

type MagentoClassifiedAttr struct {
	Attribute string                   `json:"attribute"`
	RiskClass MagentoRiskClass         `json:"riskClass"`
	Category  MagentoAttributeCategory `json:"category"`
	Rationale string                   `json:"rationale"`
	Matched   bool                     `json:"matched"`
}

type MagentoChangeClassification struct {
	RiskClass     MagentoRiskClass        `json:"riskClass"`
	ExecutionMode MagentoExecutionMode    `json:"executionMode"`
	Attributes    []MagentoClassifiedAttr `json:"attributes"`
	Reasons       []string                `json:"reasons"`
}

type ClassifyMagentoInput struct {
	Domain          MagentoDomain
	EntityType      string
	OperationID     string
	DefaultClass    MagentoRiskClass // 1 or 2
	Body            any
	Classifications []MagentoAttributeClassification
}

func ClassifyMagentoChange(input ClassifyMagentoInput) MagentoChangeClassification {
	if IsMagentoHandoffOnlyOperation(input.OperationID) {
		return MagentoChangeClassification{
			RiskClass:     0,
			ExecutionMode: MagentoHandoffOnly,
			Attributes:    []MagentoClassifiedAttr{},
			Reasons:       []string{fmt.Sprintf("%s is handoff-only in Magento V2", input.OperationID)},
		}
	}
	table := input.Classifications
	if len(table) == 0 {
		table = DefaultMagentoAttributeClassifications
	}
	attrs := map[string]any{}
	var order []string
	collectModifiedAttributes(input.Body, attrs, &order)
	attributes := make([]MagentoClassifiedAttr, 0, len(order))
	var reasons []string
	riskClass := input.DefaultClass

	for _, attr := range order {
		rawValue := attrs[attr]
		var found *MagentoAttributeClassification
		for i := range table {
			row := &table[i]
			if row.Domain == input.Domain && row.EntityType == input.EntityType && row.Attribute == attr {
				found = row
				break
			}
		}
		if found == nil {
			reason := fmt.Sprintf("Unclassified %s.%s attribute %q escalates to Class 1 human approval", input.Domain, input.EntityType, attr)
			attributes = append(attributes, MagentoClassifiedAttr{
				Attribute: attr,
				RiskClass: 1,
				Category:  MagentoCatOperational,
				Rationale: reason,
				Matched:   false,
			})
			reasons = append(reasons, reason)
			riskClass = min(riskClass, 1)
			continue
		}
		effectiveRisk := found.RiskClass
		if input.Domain == MagentoOrders && attr == "is_customer_notified" {
			notified := rawValue == true || rawValue == float64(1) || rawValue == 1 || rawValue == "1" || rawValue == "true"
			if notified {
				effectiveRisk = 1
			} else {
				effectiveRisk = 2
			}
		}
		attributes = append(attributes, MagentoClassifiedAttr{
			Attribute: attr,
			RiskClass: effectiveRisk,
			Category:  found.Category,
			Rationale: found.Rationale,
			Matched:   true,
		})
		if effectiveRisk == 0 {
			riskClass = 0
			reasons = append(reasons, found.Rationale)
		} else if effectiveRisk < riskClass {
			riskClass = effectiveRisk
			reasons = append(reasons, found.Rationale)
		}
	}

	return MagentoChangeClassification{
		RiskClass:     riskClass,
		ExecutionMode: MagentoExecutionModeFor(riskClass),
		Attributes:    attributes,
		Reasons:       reasons,
	}
}

func numberAt(value any, key string) (float64, bool) {
	if list, ok := value.([]any); ok {
		for _, item := range list {
			if found, ok := numberAt(item, key); ok {
				return found, true
			}
		}
		return 0, false
	}
	rec, ok := asStringMap(value)
	if !ok {
		return 0, false
	}
	if direct, exists := rec[key]; exists && direct != nil && direct != "" {
		if parsed, ok := toFiniteFloat(direct); ok {
			return parsed, true
		}
	}
	if attrs, ok := rec["custom_attributes"].([]any); ok {
		for _, attr := range attrs {
			if m, ok := asStringMap(attr); ok && m["attribute_code"] == key {
				if parsed, ok := toFiniteFloat(m["value"]); ok {
					return parsed, true
				}
			}
		}
	}
	for _, k := range mapKeys(value) {
		child := rec[k]
		if wrapperKeys[k] || k == "items" || k == "sourceItems" {
			if found, ok := numberAt(child, key); ok {
				return found, true
			}
		}
	}
	return 0, false
}

func toFiniteFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, !math.IsNaN(t) && !math.IsInf(t, 0)
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}

func stringAt(value any, key string) (string, bool) {
	rec, ok := asStringMap(value)
	if !ok {
		return "", false
	}
	if s, ok := rec[key].(string); ok {
		return s, true
	}
	for k, child := range rec {
		if wrapperKeys[k] {
			if found, ok := stringAt(child, key); ok {
				return found, true
			}
		}
	}
	return "", false
}

func boolAt(value any, key string) (bool, bool) {
	rec, ok := asStringMap(value)
	if !ok {
		return false, false
	}
	if direct, exists := rec[key]; exists {
		switch t := direct.(type) {
		case bool:
			return t, true
		case float64:
			if t == 1 {
				return true, true
			}
			if t == 0 {
				return false, true
			}
		case int:
			if t == 1 {
				return true, true
			}
			if t == 0 {
				return false, true
			}
		case string:
			if t == "1" || t == "true" {
				return true, true
			}
			if t == "0" || t == "false" {
				return false, true
			}
		}
	}
	for k, child := range rec {
		if wrapperKeys[k] {
			if found, ok := boolAt(child, key); ok {
				return found, true
			}
		}
	}
	return false, false
}

type MagentoChangeRow struct {
	BeforeImage any
	AfterImage  any
}

type EvaluateMagentoCapsInput struct {
	Domain            MagentoDomain
	OperationID       string
	RiskClass         MagentoRiskClass
	Rows              []MagentoChangeRow
	Caps              map[string]float64
	ProjectedExposure *float64
	AutoExecution     bool
	AutoActionsToday  int
	CooldownBreaches  []string
	Now               time.Time
}

type MagentoCapEvaluation struct {
	Allowed           bool                 `json:"allowed"`
	RiskClass         MagentoRiskClass     `json:"riskClass"`
	ExecutionMode     MagentoExecutionMode `json:"executionMode"`
	Violations        []string             `json:"violations"`
	Escalations       []string             `json:"escalations"`
	ProjectedExposure *float64             `json:"projectedExposure"`
	Snapshot          map[string]any       `json:"snapshot"`
}

func (c MagentoDomainCaps) withOverrides(overrides map[string]float64) MagentoDomainCaps {
	out := c
	if v, ok := overrides["maxRowsPerChangeset"]; ok {
		out.MaxRowsPerChangeset = v
	}
	if v, ok := overrides["maxPriceDeltaPercent"]; ok {
		out.MaxPriceDeltaPct = v
	}
	if v, ok := overrides["maxDiscountPercent"]; ok {
		out.MaxDiscountPct = v
	}
	if v, ok := overrides["maxCouponCount"]; ok {
		out.MaxCouponCount = v
	}
	if v, ok := overrides["maxProjectedExposure"]; ok {
		out.MaxProjectedExp = v
	}
	if v, ok := overrides["maxDailyAutoActions"]; ok {
		out.MaxDailyAutoActions = v
	}
	if v, ok := overrides["maxStoreCredit"]; ok {
		out.MaxStoreCredit = v
	}
	if v, ok := overrides["minPromotionDays"]; ok {
		out.MinPromotionDays = v
	}
	if v, ok := overrides["skuCooldownSeconds"]; ok {
		out.SkuCooldownSeconds = v
	}
	return out
}

func (c MagentoDomainCaps) toMap() map[string]any {
	return map[string]any{
		"maxRowsPerChangeset":  c.MaxRowsPerChangeset,
		"maxPriceDeltaPercent": c.MaxPriceDeltaPct,
		"maxDiscountPercent":   c.MaxDiscountPct,
		"maxCouponCount":       c.MaxCouponCount,
		"maxProjectedExposure": c.MaxProjectedExp,
		"maxDailyAutoActions":  c.MaxDailyAutoActions,
		"maxStoreCredit":       c.MaxStoreCredit,
		"minPromotionDays":     c.MinPromotionDays,
		"skuCooldownSeconds":   c.SkuCooldownSeconds,
	}
}

func EvaluateMagentoCaps(input EvaluateMagentoCapsInput) MagentoCapEvaluation {
	caps := DefaultMagentoCaps[input.Domain].withOverrides(input.Caps)
	violations := []string{}
	escalations := []string{}
	riskClass := input.RiskClass
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}

	if float64(len(input.Rows)) > caps.MaxRowsPerChangeset {
		violations = append(violations, fmt.Sprintf("Change-set has %d rows; domain cap is %s", len(input.Rows), formatFloat(caps.MaxRowsPerChangeset)))
	}

	var computedExposure float64
	for _, row := range input.Rows {
		beforePrice, hasBeforePrice := numberAt(row.BeforeImage, "price")
		afterPrice, hasAfterPrice := numberAt(row.AfterImage, "price")
		if hasAfterPrice && afterPrice <= 0 {
			violations = append(violations, "Product price must be greater than zero")
		}
		if hasBeforePrice && hasAfterPrice && beforePrice > 0 {
			deltaPct := (math.Abs(afterPrice-beforePrice) / beforePrice) * 100
			computedExposure += math.Abs(afterPrice - beforePrice)
			if caps.MaxPriceDeltaPct > 0 && deltaPct > caps.MaxPriceDeltaPct {
				violations = append(violations, fmt.Sprintf("Price delta %.2f%% exceeds cap of %s%%", deltaPct, formatFloat(caps.MaxPriceDeltaPct)))
			}
		}
		specialPrice, hasSpecialPrice := numberAt(row.AfterImage, "special_price")
		var refPrice float64
		hasRefPrice := false
		if hasAfterPrice {
			refPrice, hasRefPrice = afterPrice, true
		} else if hasBeforePrice {
			refPrice, hasRefPrice = beforePrice, true
		}
		if hasSpecialPrice {
			if specialPrice <= 0 {
				violations = append(violations, "Special price must be greater than zero")
			}
			if hasRefPrice && refPrice > 0 {
				discountPct := ((refPrice - specialPrice) / refPrice) * 100
				computedExposure += math.Max(0, refPrice-specialPrice)
				if caps.MaxPriceDeltaPct > 0 && discountPct > caps.MaxPriceDeltaPct {
					violations = append(violations, fmt.Sprintf("Special-price discount %.2f%% exceeds cap of %s%%", discountPct, formatFloat(caps.MaxPriceDeltaPct)))
				}
			}
		}

		afterQty, hasAfterQty := numberAt(row.AfterImage, "quantity")
		beforeQty, hasBeforeQty := numberAt(row.BeforeImage, "quantity")
		if hasAfterQty && afterQty < 0 {
			violations = append(violations, "Inventory quantity cannot be negative")
		}
		if hasBeforeQty && beforeQty > 0 && hasAfterQty && afterQty == 0 && riskClass == 2 {
			riskClass = 1
			escalations = append(escalations, "Zeroing in-stock inventory requires human approval")
		}

		discountAmount, hasDiscount := numberAt(row.AfterImage, "discount_amount")
		if hasDiscount {
			var simpleAction string
			if s, ok := stringAt(row.AfterImage, "simple_action"); ok {
				simpleAction = s
			} else if s, ok := stringAt(row.BeforeImage, "simple_action"); ok {
				simpleAction = s
			} else {
				simpleAction = "by_percent"
			}
			if discountAmount <= 0 {
				violations = append(violations, "Promotion discount must be greater than zero")
			}
			if simpleAction == "by_percent" && caps.MaxDiscountPct > 0 && discountAmount > caps.MaxDiscountPct {
				violations = append(violations, fmt.Sprintf("Promotion discount %s%% exceeds cap of %s%%", formatFloat(discountAmount), formatFloat(caps.MaxDiscountPct)))
			}
			computedExposure += discountAmount
		}

		couponQty, hasCouponQty := numberAt(row.AfterImage, "qty")
		if hasCouponQty && caps.MaxCouponCount > 0 && couponQty > caps.MaxCouponCount {
			violations = append(violations, fmt.Sprintf("Coupon generation count %s exceeds cap of %s", formatFloat(couponQty), formatFloat(caps.MaxCouponCount)))
		}

		if input.Domain == MagentoPromotions && (strings.Contains(input.OperationID, "Rule") || strings.Contains(input.OperationID, "rule")) {
			activeAfter, hasActiveAfter := boolAt(row.AfterImage, "is_active")
			activeBefore, hasActiveBefore := boolAt(row.BeforeImage, "is_active")
			var active bool
			if hasActiveAfter {
				active = activeAfter
			} else if hasActiveBefore {
				active = activeBefore
			} else {
				active = true
			}
			var toDate string
			hasToDate := false
			if s, ok := stringAt(row.AfterImage, "to_date"); ok {
				toDate, hasToDate = s, true
			} else if s, ok := stringAt(row.BeforeImage, "to_date"); ok {
				toDate, hasToDate = s, true
			}
			if active && caps.MinPromotionDays > 0 {
				if !hasToDate || strings.TrimSpace(toDate) == "" {
					violations = append(violations, "Promotion expiry is required")
				} else if expiry, ok := parseJSDate(toDate); !ok || expiry.Sub(now) < time.Duration(caps.MinPromotionDays*float64(24*time.Hour)) {
					violations = append(violations, fmt.Sprintf("Promotion expiry must be at least %s day(s) in the future", formatFloat(caps.MinPromotionDays)))
				}
			}
		}

		storeCredit, hasStoreCredit := numberAt(row.AfterImage, "store_credit_amount")
		if hasStoreCredit {
			computedExposure += storeCredit
			if storeCredit > caps.MaxStoreCredit {
				riskClass = 0
				violations = append(violations, fmt.Sprintf("Store credit %s exceeds cap of %s and is handoff-only", formatFloat(storeCredit), formatFloat(caps.MaxStoreCredit)))
			}
		}
	}

	var projectedExposure *float64
	if input.ProjectedExposure != nil {
		v := *input.ProjectedExposure
		projectedExposure = &v
	} else if computedExposure > 0 {
		v := computedExposure
		projectedExposure = &v
	}
	if projectedExposure != nil && caps.MaxProjectedExp > 0 && *projectedExposure > caps.MaxProjectedExp {
		violations = append(violations, fmt.Sprintf("Projected financial exposure %.2f exceeds cap of %s", *projectedExposure, formatFloat(caps.MaxProjectedExp)))
	}

	if input.AutoExecution {
		if riskClass != 2 {
			violations = append(violations, "Only Class 2 changes can execute automatically")
		}
		if float64(input.AutoActionsToday) >= caps.MaxDailyAutoActions {
			violations = append(violations, fmt.Sprintf("Daily automatic action cap of %s has been reached", formatFloat(caps.MaxDailyAutoActions)))
		}
		if len(input.CooldownBreaches) > 0 {
			violations = append(violations, fmt.Sprintf("Cooldown active for: %s", strings.Join(input.CooldownBreaches, ", ")))
		}
	}

	return MagentoCapEvaluation{
		Allowed:           len(violations) == 0 && riskClass != 0,
		RiskClass:         riskClass,
		ExecutionMode:     MagentoExecutionModeFor(riskClass),
		Violations:        violations,
		Escalations:       escalations,
		ProjectedExposure: projectedExposure,
		Snapshot: map[string]any{
			"caps":             caps.toMap(),
			"evaluatedAt":      now.UTC().Format("2006-01-02T15:04:05.000Z"),
			"rowCount":         len(input.Rows),
			"autoExecution":    input.AutoExecution,
			"autoActionsToday": input.AutoActionsToday,
		},
	}
}

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

var dateOnlyRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func parseJSDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if dateOnlyRe.MatchString(s) {
		t, err := time.Parse("2006-01-02", s)
		return t, err == nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
