package packs

import (
	"slices"
	"testing"
	"time"
)

func TestMagentoV2ClassificationAndCaps(t *testing.T) {
	t.Run("classifies merchandising product updates as Class 2", func(t *testing.T) {
		result := ClassifyMagentoChange(ClassifyMagentoInput{
			Domain:       MagentoCatalog,
			EntityType:   "product",
			OperationID:  "magentoUpdateProduct",
			DefaultClass: 2,
			Body: map[string]any{
				"product": map[string]any{
					"name": "Trail Runner Jacket",
					"custom_attributes": []any{
						map[string]any{"attribute_code": "short_description", "value": "Updated waterproof shell."},
					},
				},
			},
		})
		if result.RiskClass != 2 || result.ExecutionMode != MagentoControlledAutomation {
			t.Fatalf("got %+v", result)
		}
	})

	t.Run("escalates product price and visibility updates to Class 1", func(t *testing.T) {
		result := ClassifyMagentoChange(ClassifyMagentoInput{
			Domain:       MagentoCatalog,
			EntityType:   "product",
			OperationID:  "magentoUpdateProduct",
			DefaultClass: 2,
			Body: map[string]any{
				"product": map[string]any{
					"name":       "Trail Runner Jacket",
					"price":      129.99,
					"visibility": 4,
				},
			},
		})
		if result.RiskClass != 1 || result.ExecutionMode != MagentoApprovalRequired {
			t.Fatalf("got %+v", result)
		}
	})

	t.Run("escalates unclassified custom attributes to Class 1", func(t *testing.T) {
		result := ClassifyMagentoChange(ClassifyMagentoInput{
			Domain:       MagentoCatalog,
			EntityType:   "product",
			OperationID:  "magentoUpdateProduct",
			DefaultClass: 2,
			Body: map[string]any{
				"product": map[string]any{
					"custom_attributes": []any{
						map[string]any{"attribute_code": "unreviewed_erp_flag", "value": "1"},
					},
				},
			},
		})
		if result.RiskClass != 1 || len(result.Attributes) != 1 ||
			result.Attributes[0].Attribute != "unreviewed_erp_flag" ||
			result.Attributes[0].RiskClass != 1 || result.Attributes[0].Matched {
			t.Fatalf("got %+v", result)
		}
	})

	t.Run("treats customer-notifying order comments as Class 1 and internal comments as Class 2", func(t *testing.T) {
		internalComment := ClassifyMagentoChange(ClassifyMagentoInput{
			Domain:       MagentoOrders,
			EntityType:   "order_comment",
			OperationID:  "magentoAddOrderComment",
			DefaultClass: 2,
			Body: map[string]any{
				"statusHistory": map[string]any{
					"comment":              "Warehouse confirmed pallet count.",
					"is_customer_notified": 0,
				},
			},
		})
		customerComment := ClassifyMagentoChange(ClassifyMagentoInput{
			Domain:       MagentoOrders,
			EntityType:   "order_comment",
			OperationID:  "magentoAddOrderComment",
			DefaultClass: 2,
			Body: map[string]any{
				"statusHistory": map[string]any{
					"comment":              "Your order is delayed.",
					"is_customer_notified": 1,
				},
			},
		})
		if internalComment.RiskClass != 2 || customerComment.RiskClass != 1 {
			t.Fatalf("internal=%d customer=%d", internalComment.RiskClass, customerComment.RiskClass)
		}
	})

	t.Run("marks online refunds and financial configuration as handoff-only", func(t *testing.T) {
		if !IsMagentoHandoffOnlyOperation("magentoIssueOnlineCreditmemo") {
			t.Fatal("expected handoff-only")
		}
		result := ClassifyMagentoChange(ClassifyMagentoInput{
			Domain:       MagentoOrders,
			EntityType:   "creditmemo",
			OperationID:  "magentoIssueOnlineCreditmemo",
			DefaultClass: 1,
			Body:         map[string]any{"creditmemo": map[string]any{"online_refund": true}},
		})
		if result.RiskClass != 0 || result.ExecutionMode != MagentoHandoffOnly {
			t.Fatalf("got %+v", result)
		}
	})

	t.Run("enforces price delta, non-zero price, and zero-stock escalation caps", func(t *testing.T) {
		priceBlocked := EvaluateMagentoCaps(EvaluateMagentoCapsInput{
			Domain:      MagentoCatalog,
			OperationID: "magentoUpdateProduct",
			RiskClass:   1,
			Rows: []MagentoChangeRow{
				{BeforeImage: map[string]any{"price": 100.0}, AfterImage: map[string]any{"product": map[string]any{"price": 75.0}}},
			},
		})
		if priceBlocked.Allowed || !slices.Contains(priceBlocked.Violations, "Price delta 25.00% exceeds cap of 10%") {
			t.Fatalf("got %+v", priceBlocked)
		}

		zeroEscalated := EvaluateMagentoCaps(EvaluateMagentoCapsInput{
			Domain:      MagentoInventory,
			OperationID: "magentoSaveSourceItems",
			RiskClass:   2,
			Rows: []MagentoChangeRow{
				{BeforeImage: map[string]any{"quantity": 8.0}, AfterImage: map[string]any{"sourceItems": []any{map[string]any{"quantity": 0.0}}}},
			},
		})
		if !zeroEscalated.Allowed || zeroEscalated.RiskClass != 1 ||
			!slices.Contains(zeroEscalated.Escalations, "Zeroing in-stock inventory requires human approval") {
			t.Fatalf("got %+v", zeroEscalated)
		}
	})

	t.Run("enforces promotion expiry, discount, coupon count, and auto-rule caps", func(t *testing.T) {
		now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
		result := EvaluateMagentoCaps(EvaluateMagentoCapsInput{
			Domain:      MagentoPromotions,
			OperationID: "magentoCreateSalesRule",
			RiskClass:   1,
			Now:         now,
			Rows: []MagentoChangeRow{
				{
					BeforeImage: map[string]any{},
					AfterImage: map[string]any{
						"rule": map[string]any{
							"is_active":       true,
							"simple_action":   "by_percent",
							"discount_amount": 40.0,
						},
						"qty": 500.0,
					},
				},
			},
			AutoExecution:    true,
			AutoActionsToday: 10,
			CooldownBreaches: []string{"SKU-1"},
		})
		for _, want := range []string{
			"Promotion discount 40% exceeds cap of 25%",
			"Coupon generation count 500 exceeds cap of 100",
			"Promotion expiry is required",
			"Only Class 2 changes can execute automatically",
			"Daily automatic action cap of 0 has been reached",
			"Cooldown active for: SKU-1",
		} {
			if !slices.Contains(result.Violations, want) {
				t.Errorf("missing %q in %v", want, result.Violations)
			}
		}
	})

	t.Run("uses before-image promotion fields when partial updates omit them", func(t *testing.T) {
		now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
		fixedFailed := EvaluateMagentoCaps(EvaluateMagentoCapsInput{
			Domain:      MagentoPromotions,
			OperationID: "magentoUpdateSalesRule",
			RiskClass:   1,
			Now:         now,
			Rows: []MagentoChangeRow{{
				BeforeImage: map[string]any{"simple_action": "by_percent", "is_active": false, "to_date": "2026-10-01"},
				AfterImage:  map[string]any{"rule": map[string]any{"discount_amount": 40.0}},
			}},
		})
		if fixedFailed.Allowed {
			t.Fatal("expected disallowed")
		}
		fixedAllowed := EvaluateMagentoCaps(EvaluateMagentoCapsInput{
			Domain:      MagentoPromotions,
			OperationID: "magentoUpdateSalesRule",
			RiskClass:   1,
			Now:         now,
			Rows: []MagentoChangeRow{{
				BeforeImage: map[string]any{"simple_action": "by_fixed", "is_active": true, "to_date": "2026-10-01"},
				AfterImage:  map[string]any{"rule": map[string]any{"discount_amount": 40.0}},
			}},
		})
		if !fixedAllowed.Allowed {
			t.Fatalf("expected allowed: %v", fixedAllowed.Violations)
		}
		expiryCleared := EvaluateMagentoCaps(EvaluateMagentoCapsInput{
			Domain:      MagentoPromotions,
			OperationID: "magentoUpdateSalesRule",
			RiskClass:   1,
			Now:         now,
			Rows: []MagentoChangeRow{{
				BeforeImage: map[string]any{"simple_action": "by_fixed", "is_active": true, "to_date": "2026-10-01"},
				AfterImage:  map[string]any{"rule": map[string]any{"to_date": ""}},
			}},
		})
		if !slices.Contains(expiryCleared.Violations, "Promotion expiry is required") {
			t.Fatalf("got %v", expiryCleared.Violations)
		}
	})
}
