package packs

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var (
	localAcceptanceRuleRe   = regexp.MustCompile(`(?i)^Local acceptance(?:\s|$)`)
	legacyAcceptanceSummary = regexp.MustCompile(`(?i)^(?:Undo:\s*)?Acceptance\b`)
	undoPrefixRe            = regexp.MustCompile(`(?i)^Undo:\s*`)
)

func IsMagentoTestRule(name string, compiledPolicy any) bool {
	var source any
	if m, ok := asStringMap(compiledPolicy); ok {
		source = m["source"]
	}
	return source == "acceptance_test" || localAcceptanceRuleRe.MatchString(name)
}

type MagentoActivityRow struct {
	EntityRef   string `json:"entityRef"`
	BeforeImage any    `json:"beforeImage"`
	AfterImage  any    `json:"afterImage"`
}

type MagentoActivityChangeset struct {
	ID            string               `json:"id"`
	Domain        string               `json:"domain"`
	OperationID   string               `json:"operationId"`
	ExecutionMode string               `json:"executionMode"`
	Status        string               `json:"status"`
	Summary       string               `json:"summary"`
	BulkUUID      *string              `json:"bulkUuid"`
	InverseOfID   *string              `json:"inverseOfId"`
	Scope         map[string]any       `json:"scope"`
	CapSnapshot   map[string]any       `json:"capSnapshot"`
	CreatedAt     string               `json:"createdAt"`
	ReconciledAt  *string              `json:"reconciledAt"`
	Rows          []MagentoActivityRow `json:"rows"`
}

type MagentoActivityHandoff struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	EntityRef   string         `json:"entityRef"`
	Status      string         `json:"status"`
	Draft       map[string]any `json:"draft"`
	Evidence    map[string]any `json:"evidence"`
	CreatedAt   string         `json:"createdAt"`
	CompletedAt *string        `json:"completedAt"`
}

type MagentoActivityTechnical struct {
	Reference          string  `json:"reference"`
	Area               string  `json:"area"`
	Operation          string  `json:"operation"`
	Execution          string  `json:"execution"`
	OriginalRequest    string  `json:"originalRequest"`
	BulkReference      *string `json:"bulkReference"`
	InverseOfReference *string `json:"inverseOfReference"`
}

type MagentoActivityItem struct {
	ID            string                   `json:"id"`
	Kind          string                   `json:"kind"`
	Title         string                   `json:"title"`
	Description   string                   `json:"description"`
	Outcome       string                   `json:"outcome"`
	OutcomeLabel  string                   `json:"outcomeLabel"`
	AffectedCount int                      `json:"affectedCount"`
	Source        string                   `json:"source"`
	SourceLabel   string                   `json:"sourceLabel"`
	IsTest        bool                     `json:"isTest"`
	OccurredAt    string                   `json:"occurredAt"`
	CurrentState  *string                  `json:"currentState"`
	Technical     MagentoActivityTechnical `json:"technical"`
}

type changeDescriptor struct {
	requestLabel         string
	completedTitle       string
	revertedTitle        string
	completedDescription string
	revertedDescription  string
	currentState         string
}

var domainLabels = map[string]string{
	"catalog":    "Catalog",
	"content":    "Content",
	"customers":  "Customers",
	"inventory":  "Inventory",
	"orders":     "Orders",
	"promotions": "Promotions",
}

func plural(count int, singular string, pluralForm ...string) string {
	p := singular + "s"
	if len(pluralForm) > 0 && pluralForm[0] != "" {
		p = pluralForm[0]
	}
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}
	return fmt.Sprintf("%d %s", count, p)
}

func hasKey(value any, expected string) bool {
	if list, ok := value.([]any); ok {
		for _, item := range list {
			if hasKey(item, expected) {
				return true
			}
		}
		return false
	}
	rec, ok := asStringMap(value)
	if !ok {
		return false
	}
	for k, child := range rec {
		if strings.ToLower(k) == expected || hasKey(child, expected) {
			return true
		}
	}
	return false
}

func describesPriceChange(cs MagentoActivityChangeset) bool {
	if strings.Contains(cs.OperationID, "price") {
		return true
	}
	for _, row := range cs.Rows {
		if hasKey(row.AfterImage, "price") || hasKey(row.BeforeImage, "price") {
			return true
		}
	}
	return false
}

func descriptor(cs MagentoActivityChangeset) changeDescriptor {
	count := len(cs.Rows)
	op := cs.OperationID

	if cs.Domain == "catalog" && describesPriceChange(cs) {
		ct, rt := "Prices updated", "Prices restored"
		if count == 1 {
			ct, rt = "Price updated", "Price restored"
		}
		return changeDescriptor{
			requestLabel:         "Price update",
			completedTitle:       ct,
			revertedTitle:        rt,
			completedDescription: fmt.Sprintf("Updated prices for %s.", plural(count, "product")),
			revertedDescription:  fmt.Sprintf("Restored the original prices for %s.", plural(count, "product")),
			currentState:         "No current price change",
		}
	}

	if strings.HasPrefix(op, "product_") {
		ct, rt := "Products updated", "Products restored"
		if count == 1 {
			ct, rt = "Product updated", "Product restored"
		}
		return changeDescriptor{
			requestLabel:         "Product update",
			completedTitle:       ct,
			revertedTitle:        rt,
			completedDescription: fmt.Sprintf("Updated %s.", plural(count, "product")),
			revertedDescription:  fmt.Sprintf("Restored the original details for %s.", plural(count, "product")),
			currentState:         "No current product change",
		}
	}

	if strings.Contains(op, "category") {
		ct, rt := "Categories updated", "Categories restored"
		if count == 1 {
			ct, rt = "Category updated", "Category restored"
		}
		return changeDescriptor{
			requestLabel:         "Category update",
			completedTitle:       ct,
			revertedTitle:        rt,
			completedDescription: fmt.Sprintf("Updated %s.", plural(count, "category", "categories")),
			revertedDescription:  fmt.Sprintf("Restored the original category details for %s.", plural(count, "category", "categories")),
			currentState:         "No current category change",
		}
	}

	if cs.Domain == "inventory" {
		return changeDescriptor{
			requestLabel:         "Inventory update",
			completedTitle:       "Inventory updated",
			revertedTitle:        "Inventory restored",
			completedDescription: fmt.Sprintf("Updated %s.", plural(count, "inventory item")),
			revertedDescription:  fmt.Sprintf("Restored the original values for %s.", plural(count, "inventory item")),
			currentState:         "No current inventory change",
		}
	}

	if cs.Domain == "orders" {
		ct, rt := "Orders updated", "Orders restored"
		if count == 1 {
			ct, rt = "Order updated", "Order restored"
		}
		return changeDescriptor{
			requestLabel:         "Order update",
			completedTitle:       ct,
			revertedTitle:        rt,
			completedDescription: fmt.Sprintf("Updated %s.", plural(count, "order")),
			revertedDescription:  fmt.Sprintf("Restored the original state for %s.", plural(count, "order")),
			currentState:         "No current order change",
		}
	}

	if cs.Domain == "promotions" {
		target := "promotion"
		if strings.Contains(op, "coupon") {
			target = "coupon"
		}
		rl, ct, rt := "Promotion update", "Promotion updated", "Promotion restored"
		if target == "coupon" {
			rl, ct, rt = "Coupon update", "Coupons updated", "Coupons restored"
		}
		return changeDescriptor{
			requestLabel:         rl,
			completedTitle:       ct,
			revertedTitle:        rt,
			completedDescription: fmt.Sprintf("Updated %s.", plural(count, target)),
			revertedDescription:  fmt.Sprintf("Restored the original details for %s.", plural(count, target)),
			currentState:         fmt.Sprintf("No current %s change", target),
		}
	}

	if cs.Domain == "content" {
		target := "content block"
		if strings.Contains(op, "page") {
			target = "content page"
		}
		return changeDescriptor{
			requestLabel:         "Content update",
			completedTitle:       "Content updated",
			revertedTitle:        "Content restored",
			completedDescription: fmt.Sprintf("Updated %s.", plural(count, target)),
			revertedDescription:  fmt.Sprintf("Restored the original content for %s.", plural(count, target)),
			currentState:         "No current content change",
		}
	}

	if cs.Domain == "customers" {
		ct, rt := "Customers updated", "Customers restored"
		if count == 1 {
			ct, rt = "Customer updated", "Customer restored"
		}
		return changeDescriptor{
			requestLabel:         "Customer update",
			completedTitle:       ct,
			revertedTitle:        rt,
			completedDescription: fmt.Sprintf("Updated %s.", plural(count, "customer")),
			revertedDescription:  fmt.Sprintf("Restored the original details for %s.", plural(count, "customer")),
			currentState:         "No current customer change",
		}
	}

	area := domainLabels[cs.Domain]
	if area == "" {
		area = "Store"
	}
	return changeDescriptor{
		requestLabel:         fmt.Sprintf("%s update", area),
		completedTitle:       fmt.Sprintf("%s updated", area),
		revertedTitle:        fmt.Sprintf("%s restored", area),
		completedDescription: fmt.Sprintf("Completed %s.", plural(count, "store change")),
		revertedDescription:  fmt.Sprintf("Restored the original state for %s.", plural(count, "store change")),
		currentState:         fmt.Sprintf("No current %s change", strings.ToLower(area)),
	}
}

func changesetSource(cs MagentoActivityChangeset) (string, string, bool) {
	if cs.Scope["activityOrigin"] == "acceptance_test" || legacyAcceptanceSummary.MatchString(cs.Summary) {
		return "test", "Local test", true
	}
	if _, ok := cs.CapSnapshot["autoRuleId"].(string); ok {
		return "automatic_rule", "Automatic rule", false
	}
	return "requested_change", "Requested change", false
}

func strPtr(s string) *string { return &s }

func statusPresentation(cs MagentoActivityChangeset, change changeDescriptor) (title, desc, outcome, outcomeLabel string, currentState *string) {
	isCompletedUndo := cs.InverseOfID != nil && *cs.InverseOfID != "" && cs.Status == "applied"
	if isCompletedUndo {
		return change.revertedTitle, change.revertedDescription, "reverted", "Reverted", strPtr(change.currentState)
	}
	if cs.Status == "applied" {
		return change.completedTitle, change.completedDescription, "completed", "Completed", nil
	}
	if cs.Status == "pending_approval" || cs.Status == "previewed" {
		return fmt.Sprintf("%s awaiting approval", change.requestLabel),
			"An administrator needs to review this change before anything is updated in Magento.",
			"awaiting_approval", "Awaiting approval", strPtr("Nothing changed yet")
	}
	if cs.Status == "approved" || cs.Status == "executing" {
		return fmt.Sprintf("%s in progress", change.requestLabel),
			"Magento is applying the approved change now.",
			"in_progress", "In progress", nil
	}
	if cs.Status == "partially_applied" || cs.Status == "reconcile_required" {
		return fmt.Sprintf("%s needs attention", change.requestLabel),
			"Magento could not confirm every requested update. Review the technical details before trying again.",
			"needs_attention", "Needs attention", strPtr("Current state needs review")
	}
	if cs.Status == "failed" {
		return fmt.Sprintf("%s failed", change.requestLabel),
			"The requested change could not be completed in Magento.",
			"failed", "Failed", strPtr("Current store data was not confirmed")
	}
	return fmt.Sprintf("%s cancelled", change.requestLabel),
		"The requested change was cancelled before it completed.",
		"cancelled", "Cancelled", strPtr("Nothing changed")
}

func changeActivity(cs MagentoActivityChangeset) MagentoActivityItem {
	change := descriptor(cs)
	title, desc, outcome, outcomeLabel, currentState := statusPresentation(cs, change)
	source, sourceLabel, isTest := changesetSource(cs)
	occurredAt := cs.CreatedAt
	if cs.ReconciledAt != nil && *cs.ReconciledAt != "" {
		occurredAt = *cs.ReconciledAt
	}
	area := domainLabels[cs.Domain]
	if area == "" {
		area = cs.Domain
	}
	return MagentoActivityItem{
		ID:            cs.ID,
		Kind:          "change",
		Title:         title,
		Description:   desc,
		Outcome:       outcome,
		OutcomeLabel:  outcomeLabel,
		AffectedCount: len(cs.Rows),
		Source:        source,
		SourceLabel:   sourceLabel,
		IsTest:        isTest,
		OccurredAt:    occurredAt,
		CurrentState:  currentState,
		Technical: MagentoActivityTechnical{
			Reference:          cs.ID,
			Area:               area,
			Operation:          cs.OperationID,
			Execution:          cs.ExecutionMode,
			OriginalRequest:    undoPrefixRe.ReplaceAllString(cs.Summary, ""),
			BulkReference:      cs.BulkUUID,
			InverseOfReference: cs.InverseOfID,
		},
	}
}

var handoffLabels = map[string][2]string{
	"online_refund": {
		"Refund ready in Magento Admin",
		"Review the evidence and complete the refund in Magento Admin.",
	},
	"return_approval": {
		"Return ready for review",
		"Review and approve the return in Magento Admin.",
	},
	"financial_configuration": {
		"Financial setting ready for review",
		"Review and make this sensitive configuration change in Magento Admin.",
	},
	"store_credit_over_cap": {
		"Store credit needs administrator review",
		"Review the evidence and complete the store credit change in Magento Admin.",
	},
}

func handoffActivity(h MagentoActivityHandoff) MagentoActivityItem {
	copyPair, ok := handoffLabels[h.Kind]
	if !ok {
		copyPair = [2]string{
			"Magento task ready for review",
			"Review the evidence and complete this task in Magento Admin.",
		}
	}
	completed := h.Status == "completed_by_human"
	cancelled := h.Status == "cancelled"
	reasonStr := ""
	if r, exists := h.Evidence["reason"]; exists && r != nil {
		reasonStr = fmt.Sprint(r)
	}
	isTest := h.Draft["testOnly"] == true || strings.Contains(reasonStr, "acceptance")
	title := copyPair[0]
	if completed {
		title = strings.Replace(title, "ready", "completed", 1)
	}
	outcome := "awaiting_approval"
	outcomeLabel := "Action needed"
	if cancelled {
		outcome = "cancelled"
		outcomeLabel = "Cancelled"
	} else if completed {
		outcome = "completed"
		outcomeLabel = "Completed"
	}
	source := "requested_change"
	sourceLabel := "Requested change"
	if isTest {
		source = "test"
		sourceLabel = "Local test"
	}
	occurredAt := h.CreatedAt
	if h.CompletedAt != nil && *h.CompletedAt != "" {
		occurredAt = *h.CompletedAt
	}
	var currentState *string
	if !completed {
		currentState = strPtr("OpenNeko did not change Magento")
	}
	return MagentoActivityItem{
		ID:            h.ID,
		Kind:          "handoff",
		Title:         title,
		Description:   copyPair[1],
		Outcome:       outcome,
		OutcomeLabel:  outcomeLabel,
		AffectedCount: 1,
		Source:        source,
		SourceLabel:   sourceLabel,
		IsTest:        isTest,
		OccurredAt:    occurredAt,
		CurrentState:  currentState,
		Technical: MagentoActivityTechnical{
			Reference:          h.ID,
			Area:               "Financial handoff",
			Operation:          h.Kind,
			Execution:          "Complete in Magento Admin",
			OriginalRequest:    h.EntityRef,
			BulkReference:      nil,
			InverseOfReference: nil,
		},
	}
}

func activityTimestamp(s string) int64 {
	if t, ok := parseJSDate(s); ok {
		return t.UnixMilli()
	}
	return 0
}

func BuildMagentoActivity(changesets []MagentoActivityChangeset, handoffs []MagentoActivityHandoff) []MagentoActivityItem {
	successfullyReverted := map[string]bool{}
	for _, cs := range changesets {
		if cs.InverseOfID != nil && *cs.InverseOfID != "" && cs.Status == "applied" {
			successfullyReverted[*cs.InverseOfID] = true
		}
	}
	items := make([]MagentoActivityItem, 0, len(changesets)+len(handoffs))
	for _, cs := range changesets {
		if !successfullyReverted[cs.ID] {
			items = append(items, changeActivity(cs))
		}
	}
	for _, h := range handoffs {
		items = append(items, handoffActivity(h))
	}
	slices.SortStableFunc(items, func(left, right MagentoActivityItem) int {
		lt := activityTimestamp(left.OccurredAt)
		rt := activityTimestamp(right.OccurredAt)
		if rt > lt {
			return 1
		}
		if rt < lt {
			return -1
		}
		return 0
	})
	return items
}

func OperatorReadinessDetail(reason string) string {
	switch reason {
	case "ready":
		return "Changes can run under the configured store limits."
	case "read_only_role":
		return "View-only access is active; the Magento login does not have permission to make changes in this area."
	case "integration_token_invalid":
		return "View-only access is active because Magento rejected the integration token."
	case "integration_token_missing":
		return "View-only access is active because no Magento integration token is configured."
	case "magento_unreachable":
		return "View-only access is active because Magento could not be reached."
	case "magento_version_unsupported":
		return "View-only access is active because the Magento version could not be verified for safe changes."
	case "graphjin_source_unavailable":
		return "View-only access is active because the Magento change connection is unavailable."
	case "async_bulk_consumer_stalled":
		return "Bulk updates are paused because Magento has unfinished background work."
	case "domain_disabled":
		return "This change domain is disabled by the administrator."
	default:
		return "View-only access could not be checked because the reporting connection is unavailable."
	}
}
