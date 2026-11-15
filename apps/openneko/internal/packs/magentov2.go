package packs

import (
	"fmt"
	"maps"
	"math"
	"math/big"
	"reflect"
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

var magentoHandoffOnlyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)refund`),
	regexp.MustCompile(`(?i)return.*approve|approve.*return|rma.*approve`),
	regexp.MustCompile(`(?i)payment.*config|tax.*config|currency.*rate`),
	regexp.MustCompile(`(?i)integration|admin.*user|authorization.*role`),
}

func IsMagentoHandoffOnlyOperation(operationID string) bool {
	for _, pattern := range magentoHandoffOnlyPatterns {
		if pattern.MatchString(operationID) {
			return true
		}
	}
	return false
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

// OrderedObject is a JSON object that keeps JavaScript key order. The price
// check pairs before and after values by position, so plain maps sort keys.
type OrderedObject interface {
	ObjectKeys() []string
	ObjectValue(key string) any
}

func magentoEntries(v any) ([]string, func(string) any, bool) {
	switch t := v.(type) {
	case OrderedObject:
		if t == nil || reflect.ValueOf(t).IsNil() {
			return nil, nil, false
		}
		return t.ObjectKeys(), t.ObjectValue, true
	case map[string]any:
		if t == nil {
			return nil, nil, false
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, utf16Less)
		return keys, func(k string) any { return t[k] }, true
	}
	return nil, nil, false
}

var magentoStructuralKeys = map[string]bool{
	"product": true, "category": true, "sourceitems": true, "source_items": true,
	"statushistory": true, "salesrule": true, "rule": true, "coupon": true,
	"page": true, "block": true, "customer": true, "entity": true,
	"extension_attributes": true, "custom_attributes": true, "items": true,
}

func MagentoLeafAttributes(value any) []string {
	seen := map[string]bool{}
	var visit func(item any, key string, hasKey bool)
	visit = func(item any, key string, hasKey bool) {
		if list, ok := item.([]any); ok {
			for _, child := range list {
				visit(child, key, hasKey)
			}
			return
		}
		if keys, get, ok := magentoEntries(item); ok {
			if code, ok := get("attribute_code").(string); ok && slices.Contains(keys, "value") {
				seen[strings.ToLower(code)] = true
			}
			for _, k := range keys {
				visit(get(k), k, true)
			}
			return
		}
		if hasKey && key != "" && !magentoStructuralKeys[strings.ToLower(key)] {
			seen[strings.ToLower(key)] = true
		}
	}
	visit(value, "", false)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	slices.SortFunc(out, utf16Less)
	return out
}

type MagentoClassifiedAttr struct {
	Attribute string                   `json:"attribute"`
	RiskClass MagentoRiskClass         `json:"riskClass"`
	Category  MagentoAttributeCategory `json:"category"`
	Reviewed  bool                     `json:"reviewed"`
}

type MagentoChangeClassification struct {
	RiskClass  MagentoRiskClass        `json:"riskClass"`
	Attributes []MagentoClassifiedAttr `json:"attributes"`
	Reasons    []string                `json:"reasons"`
}

type ClassifyMagentoInput struct {
	Domain          MagentoDomain
	EntityType      string
	OperationID     string
	DefaultClass    MagentoRiskClass
	Body            any
	Classifications []MagentoAttributeClassification
}

func ClassifyMagentoChange(input ClassifyMagentoInput) MagentoChangeClassification {
	if IsMagentoHandoffOnlyOperation(input.OperationID) {
		return MagentoChangeClassification{
			RiskClass:  0,
			Attributes: []MagentoClassifiedAttr{},
			Reasons:    []string{"This operation must be completed by a human in Magento Admin"},
		}
	}
	table := input.Classifications
	if table == nil {
		table = DefaultMagentoAttributeClassifications
	}
	byAttribute := map[string]MagentoAttributeClassification{}
	for _, entry := range table {
		if entry.Domain == input.Domain && (entry.EntityType == input.EntityType || entry.EntityType == "*") {
			byAttribute[strings.ToLower(entry.Attribute)] = entry
		}
	}
	riskClass := input.DefaultClass
	attributes := []MagentoClassifiedAttr{}
	reasons := []string{}
	for _, attr := range MagentoLeafAttributes(input.Body) {
		entry, ok := byAttribute[attr]
		classified := MagentoClassifiedAttr{Attribute: attr, RiskClass: 1, Category: MagentoCatOperational}
		if ok {
			classified = MagentoClassifiedAttr{Attribute: attr, RiskClass: entry.RiskClass, Category: entry.Category, Reviewed: true}
		} else {
			reasons = append(reasons, fmt.Sprintf("Unreviewed attribute %s requires administrator approval", attr))
		}
		riskClass = min(riskClass, classified.RiskClass)
		attributes = append(attributes, classified)
	}
	return MagentoChangeClassification{RiskClass: riskClass, Attributes: attributes, Reasons: reasons}
}

var magentoCapKeys = []string{
	"maxRowsPerChangeset", "maxPriceDeltaPercent", "maxDiscountPercent", "maxCouponCount",
	"maxProjectedExposure", "maxDailyAutoActions", "maxStoreCredit", "minPromotionDays", "skuCooldownSeconds",
}

var DefaultMagentoCaps = map[string]float64{
	"maxRowsPerChangeset":  500,
	"maxPriceDeltaPercent": 10,
	"maxDiscountPercent":   30,
	"maxCouponCount":       500,
	"maxProjectedExposure": 5000,
	"maxDailyAutoActions":  25,
	"maxStoreCredit":       50,
	"minPromotionDays":     1,
	"skuCooldownSeconds":   3600,
}

var jsDecimalRe = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)

func jsStringNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, true
	}
	if len(s) > 2 && s[0] == '0' {
		base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[s[1]]
		if base != 0 {
			n, ok := new(big.Int).SetString(s[2:], base)
			if !ok {
				return 0, false
			}
			f, _ := new(big.Float).SetInt(n).Float64()
			return f, !math.IsInf(f, 0)
		}
	}
	if !jsDecimalRe.MatchString(s) {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil && !math.IsInf(f, 0)
}

func jsArrayString(list []any) (string, bool) {
	parts := make([]string, len(list))
	for i, item := range list {
		switch t := item.(type) {
		case nil:
		case string:
			parts[i] = t
		case float64:
			parts[i] = numberText(t)
		case bool:
			parts[i] = strconv.FormatBool(t)
		case []any:
			s, ok := jsArrayString(t)
			if !ok {
				return "", false
			}
			parts[i] = s
		default:
			return "", false
		}
	}
	return strings.Join(parts, ","), true
}

func magentoFiniteNumber(value any) (float64, bool) {
	switch t := value.(type) {
	case nil:
		return 0, false
	case float64:
		return t, !math.IsNaN(t) && !math.IsInf(t, 0)
	case int:
		return float64(t), true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case string:
		if t == "" {
			return 0, false
		}
		return jsStringNumber(t)
	case []any:
		s, ok := jsArrayString(t)
		if !ok {
			return 0, false
		}
		return jsStringNumber(s)
	}
	return 0, false
}

func magentoValuesForKeys(value any, keys ...string) []float64 {
	values := []float64{}
	var visit func(item any)
	visit = func(item any) {
		if list, ok := item.([]any); ok {
			for _, child := range list {
				visit(child)
			}
			return
		}
		names, get, ok := magentoEntries(item)
		if !ok {
			return
		}
		for _, k := range names {
			child := get(k)
			if slices.Contains(keys, strings.ToLower(k)) {
				if n, ok := magentoFiniteNumber(child); ok {
					values = append(values, n)
				}
			}
			visit(child)
		}
	}
	visit(value)
	return values
}

func magentoStringForKeys(value any, keys ...string) string {
	if list, ok := value.([]any); ok {
		for _, child := range list {
			if found := magentoStringForKeys(child, keys...); found != "" {
				return found
			}
		}
		return ""
	}
	names, get, ok := magentoEntries(value)
	if !ok {
		return ""
	}
	for _, k := range names {
		child := get(k)
		if s, isString := child.(string); isString && slices.Contains(keys, strings.ToLower(k)) && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
		if found := magentoStringForKeys(child, keys...); found != "" {
			return found
		}
	}
	return ""
}

type magentoSpread struct {
	keys []string
	vals map[string]any
}

func (o *magentoSpread) ObjectKeys() []string       { return o.keys }
func (o *magentoSpread) ObjectValue(key string) any { return o.vals[key] }

func (o *magentoSpread) add(v any) {
	if list, ok := v.([]any); ok {
		for i, item := range list {
			o.set(strconv.Itoa(i), item)
		}
		return
	}
	if keys, get, ok := magentoEntries(v); ok {
		for _, k := range keys {
			o.set(k, get(k))
		}
	}
}

func (o *magentoSpread) set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func magentoEffectiveRule(before, after any) *magentoSpread {
	rule := &magentoSpread{vals: map[string]any{}}
	rule.add(before)
	if keys, get, ok := magentoEntries(after); ok && slices.Contains(keys, "rule") {
		rule.add(get("rule"))
	} else {
		rule.add(after)
	}
	return rule
}

func jsToFixed2(f float64) string {
	r := new(big.Rat).SetFloat64(f)
	r.Mul(r, big.NewRat(100, 1))
	r.Add(r, big.NewRat(1, 2))
	n := new(big.Int).Div(r.Num(), r.Denom())
	s := n.String()
	for len(s) < 3 {
		s = "0" + s
	}
	return s[:len(s)-2] + "." + s[len(s)-2:]
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
	Allowed           bool             `json:"allowed"`
	RiskClass         MagentoRiskClass `json:"riskClass"`
	Violations        []string         `json:"violations"`
	Escalations       []string         `json:"escalations"`
	ProjectedExposure *float64         `json:"projectedExposure"`
	Snapshot          map[string]any   `json:"snapshot"`
}

func EvaluateMagentoCaps(input EvaluateMagentoCapsInput) MagentoCapEvaluation {
	caps := maps.Clone(DefaultMagentoCaps)
	maps.Copy(caps, input.Caps)
	violations := []string{}
	escalations := []string{}
	riskClass := input.RiskClass
	if float64(len(input.Rows)) > caps["maxRowsPerChangeset"] {
		violations = append(violations, fmt.Sprintf("Change-set has %d rows; maximum is %s", len(input.Rows), numberText(caps["maxRowsPerChangeset"])))
	}

	for _, row := range input.Rows {
		before := magentoValuesForKeys(row.BeforeImage, "price", "special_price", "value")
		after := magentoValuesForKeys(row.AfterImage, "price", "special_price", "value")
		for i := 0; i < min(len(before), len(after)); i++ {
			prior, next := before[i], after[i]
			if prior <= 0 {
				continue
			}
			delta := math.Abs((next - prior) / prior * 100)
			if delta > caps["maxPriceDeltaPercent"] {
				riskClass = min(riskClass, 1)
				escalations = append(escalations, fmt.Sprintf(
					"Price delta %s%% exceeds the %s%% automatic-execution limit and requires administrator approval",
					jsToFixed2(delta), numberText(caps["maxPriceDeltaPercent"])))
			}
		}
	}

	if input.Domain == MagentoPromotions {
		var discounts, couponCounts []float64
		for _, row := range input.Rows {
			discounts = append(discounts, magentoValuesForKeys(row.AfterImage, "discount_amount")...)
			couponCounts = append(couponCounts, magentoValuesForKeys(row.AfterImage, "quantity", "qty", "coupon_count")...)
		}
		if slices.ContainsFunc(discounts, func(v float64) bool { return v >= 90 }) {
			violations = append(violations, "A promotion that can create a free or near-free cart is not executable")
			riskClass = 0
		} else if slices.ContainsFunc(discounts, func(v float64) bool { return v > caps["maxDiscountPercent"] }) {
			violations = append(violations, fmt.Sprintf("Promotion discount exceeds the %s%% ceiling", numberText(caps["maxDiscountPercent"])))
		}
		if slices.ContainsFunc(couponCounts, func(v float64) bool { return v > caps["maxCouponCount"] }) {
			violations = append(violations, fmt.Sprintf("Coupon count exceeds the %s ceiling", numberText(caps["maxCouponCount"])))
		}
		for _, row := range input.Rows {
			if input.OperationID == "magentoDeleteSalesRule" || input.OperationID == "magentoGenerateCoupons" {
				continue
			}
			rule := magentoEffectiveRule(row.BeforeImage, row.AfterImage)
			start := magentoStringForKeys(rule, "from_date")
			end := magentoStringForKeys(rule, "to_date")
			if end == "" {
				violations = append(violations, "Promotion expiry is required")
			}
			if start != "" && end != "" {
				from, okFrom := parseJSDate(start)
				to, okTo := parseJSDate(end)
				if okFrom && okTo && to.Sub(from).Hours()/24 < caps["minPromotionDays"] {
					violations = append(violations, fmt.Sprintf("Promotion duration must be at least %s day(s)", numberText(caps["minPromotionDays"])))
				}
			}
		}
	}

	var projectedExposure *float64
	if input.ProjectedExposure != nil && !math.IsNaN(*input.ProjectedExposure) && !math.IsInf(*input.ProjectedExposure, 0) {
		v := *input.ProjectedExposure
		projectedExposure = &v
		if v > caps["maxProjectedExposure"] {
			violations = append(violations, fmt.Sprintf("Projected exposure %s exceeds the %s ceiling", numberText(v), numberText(caps["maxProjectedExposure"])))
		}
	}

	if input.AutoExecution {
		if riskClass != 2 {
			violations = append(violations, "This change requires administrator approval and cannot execute automatically")
		}
		if float64(input.AutoActionsToday) >= caps["maxDailyAutoActions"] {
			violations = append(violations, fmt.Sprintf("Daily automatic-action cap of %s is exhausted", numberText(caps["maxDailyAutoActions"])))
		}
		for _, entity := range input.CooldownBreaches {
			violations = append(violations, "Cooldown is still active for "+entity)
		}
	}

	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	snapshotCaps := map[string]any{}
	for k, v := range caps {
		snapshotCaps[k] = v
	}
	return MagentoCapEvaluation{
		Allowed:           len(violations) == 0 && riskClass != 0,
		RiskClass:         riskClass,
		Violations:        violations,
		Escalations:       escalations,
		ProjectedExposure: projectedExposure,
		Snapshot: map[string]any{
			"caps":             snapshotCaps,
			"evaluatedAt":      now.UTC().Format("2006-01-02T15:04:05.000Z"),
			"rowCount":         len(input.Rows),
			"autoExecution":    input.AutoExecution,
			"autoActionsToday": input.AutoActionsToday,
		},
	}
}

var dateOnlyRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func parseJSDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if dateOnlyRe.MatchString(s) {
		t, err := time.Parse("2006-01-02", s)
		return t, err == nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func asStringMap(v any) (map[string]any, bool) {
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	return nil, false
}
