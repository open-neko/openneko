import { describe, expect, it } from "vitest";
import { nativeArtifactStateHash, policyArtifactStateHash } from "../src/packs/artifact-state";

describe("pack materialized state", () => {
  it("ignores database timestamps but detects managed metric edits", () => {
    const original = {
      role: "owner",
      slug: "orders",
      title: "Orders",
      active: true,
      updated_at: new Date("2026-01-01"),
    };
    expect(nativeArtifactStateHash("metric", {
      ...original,
      updated_at: new Date("2026-08-20"),
    })).toBe(nativeArtifactStateHash("metric", original));
    expect(nativeArtifactStateHash("metric", {
      ...original,
      title: "Orders edited by user",
    })).not.toBe(nativeArtifactStateHash("metric", original));
  });

  it("accepts legacy policy receipts without hiding actual policy edits", () => {
    const policy = {
      name: "Magento governed store changes",
      description: "Approval required",
      applies_to_kinds: ["magento.manage_catalog"],
      applies_to_scopes: ["external"],
      mode: "approval_required",
      allowed_targets: { source: "magento_operator" },
      limits: { retry: "never" },
      priority: 10,
      enabled: false,
    };
    const legacyReceipt = nativeArtifactStateHash("policy", { ...policy, approver_role: "admin" });
    expect(policyArtifactStateHash(policy, legacyReceipt)).toBe(legacyReceipt);
    expect(policyArtifactStateHash({ ...policy, limits: { retry: "always" } }, legacyReceipt))
      .not.toBe(legacyReceipt);
    expect(policyArtifactStateHash(policy)).toBe(nativeArtifactStateHash("policy", policy));
  });
});
