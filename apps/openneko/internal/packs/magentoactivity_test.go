package packs

import "testing"

func testChangeset(overrides MagentoActivityChangeset) MagentoActivityChangeset {
	cs := MagentoActivityChangeset{
		ID:            "cs-1",
		Domain:        "catalog",
		OperationID:   "product_update",
		ExecutionMode: "controlled_automation_eligible",
		Status:        "applied",
		Summary:       "Raise price on low-stock products",
		Scope:         map[string]any{},
		CapSnapshot:   map[string]any{},
		CreatedAt:     "2026-09-13T10:00:00.000Z",
		ReconciledAt:  strPtr("2026-09-13T10:01:00.000Z"),
		Rows: []MagentoActivityRow{{
			EntityRef:   "SKU-1",
			BeforeImage: map[string]any{"price": 100.0},
			AfterImage:  map[string]any{"product": map[string]any{"price": 102.0}},
		}},
	}
	if overrides.ID != "" {
		cs.ID = overrides.ID
	}
	if overrides.Domain != "" {
		cs.Domain = overrides.Domain
	}
	if overrides.OperationID != "" {
		cs.OperationID = overrides.OperationID
	}
	if overrides.ExecutionMode != "" {
		cs.ExecutionMode = overrides.ExecutionMode
	}
	if overrides.Status != "" {
		cs.Status = overrides.Status
	}
	if overrides.Summary != "" {
		cs.Summary = overrides.Summary
	}
	if overrides.BulkUUID != nil {
		cs.BulkUUID = overrides.BulkUUID
	}
	if overrides.InverseOfID != nil {
		cs.InverseOfID = overrides.InverseOfID
	}
	if overrides.Scope != nil {
		cs.Scope = overrides.Scope
	}
	if overrides.CapSnapshot != nil {
		cs.CapSnapshot = overrides.CapSnapshot
	}
	if overrides.CreatedAt != "" {
		cs.CreatedAt = overrides.CreatedAt
	}
	if overrides.ReconciledAt != nil {
		cs.ReconciledAt = overrides.ReconciledAt
	}
	if overrides.Rows != nil {
		cs.Rows = overrides.Rows
	}
	return cs
}

func TestMagentoActivityPresentation(t *testing.T) {
	t.Run("replaces a reverted change with one current-state activity row", func(t *testing.T) {
		activity := BuildMagentoActivity([]MagentoActivityChangeset{
			testChangeset(MagentoActivityChangeset{
				ID:           "cs-original",
				Summary:      "Acceptance catalog price update",
				Scope:        map[string]any{"activityOrigin": "acceptance_test"},
				CreatedAt:    "2026-09-13T10:00:00.000Z",
				ReconciledAt: strPtr("2026-09-13T10:01:00.000Z"),
			}),
			testChangeset(MagentoActivityChangeset{
				ID:           "cs-undo",
				InverseOfID:  strPtr("cs-original"),
				Summary:      "Undo: Acceptance catalog price update",
				Scope:        map[string]any{"activityOrigin": "acceptance_test"},
				CreatedAt:    "2026-09-13T10:02:00.000Z",
				ReconciledAt: strPtr("2026-09-13T10:03:00.000Z"),
			}),
		}, nil)
		if len(activity) != 1 {
			t.Fatalf("len=%d", len(activity))
		}
		got := activity[0]
		if got.ID != "cs-undo" || got.Title != "Price restored" ||
			got.Description != "Restored the original prices for 1 product." ||
			got.Outcome != "reverted" || got.OutcomeLabel != "Reverted" ||
			got.CurrentState == nil || *got.CurrentState != "No current price change" ||
			!got.IsTest || got.Source != "test" || got.SourceLabel != "Local test" ||
			got.Technical.OriginalRequest != "Acceptance catalog price update" ||
			got.Technical.InverseOfReference == nil || *got.Technical.InverseOfReference != "cs-original" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("describes live requested and automatic changes in store language", func(t *testing.T) {
		activity := BuildMagentoActivity(
			[]MagentoActivityChangeset{
				testChangeset(MagentoActivityChangeset{
					ID:          "cs-auto",
					Domain:      "inventory",
					OperationID: "source_items_save",
					Summary:     "Restock warehouse bin",
					CapSnapshot: map[string]any{"autoRuleId": "rule-1"},
					Rows: []MagentoActivityRow{
						{EntityRef: "SKU-1", BeforeImage: map[string]any{"quantity": 2.0}, AfterImage: map[string]any{"quantity": 10.0}},
						{EntityRef: "SKU-2", BeforeImage: map[string]any{"quantity": 1.0}, AfterImage: map[string]any{"quantity": 5.0}},
					},
				}),
			},
			[]MagentoActivityHandoff{
				{
					ID:        "handoff-1",
					Kind:      "online_refund",
					EntityRef: "000000101",
					Status:    "ready_for_human",
					Draft:     map[string]any{"amount": 49.0},
					Evidence:  map[string]any{"reason": "Customer return"},
					CreatedAt: "2026-09-13T11:00:00.000Z",
				},
			},
		)
		if len(activity) != 2 {
			t.Fatalf("len=%d", len(activity))
		}
		if activity[0].ID != "handoff-1" || activity[0].Title != "Refund ready in Magento Admin" ||
			activity[0].OutcomeLabel != "Action needed" || activity[0].CurrentState == nil ||
			*activity[0].CurrentState != "OpenNeko did not change Magento" || activity[0].IsTest {
			t.Fatalf("got[0] %+v", activity[0])
		}
		if activity[1].ID != "cs-auto" || activity[1].Title != "Inventory updated" ||
			activity[1].Description != "Updated 2 inventory items." ||
			activity[1].Source != "automatic_rule" || activity[1].SourceLabel != "Automatic rule" ||
			activity[1].IsTest {
			t.Fatalf("got[1] %+v", activity[1])
		}
	})

	t.Run("marks acceptance-tagged rules as hidden test fixtures", func(t *testing.T) {
		if !IsMagentoTestRule("Catalog rule", map[string]any{"source": "acceptance_test"}) {
			t.Fatal("expected test rule")
		}
		if !IsMagentoTestRule("Local acceptance catalog rule", map[string]any{}) {
			t.Fatal("expected test rule by name")
		}
		if IsMagentoTestRule("Low-stock price correction", map[string]any{"source": "admin_plain_language"}) {
			t.Fatal("expected non-test rule")
		}
	})
}
