import { describe, expect, it } from "vitest";
import type { ReactElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { applyMessage } from "@/a2ui/surface";
import { renderComponent } from "@/a2ui/renderer";
import { WORK_CATALOG_ID } from "@/a2ui/catalog";
import { packAppliedFollowUp } from "@/a2ui/pack-setup";
import { buildActionFollowUp } from "@/a2ui/action";
import "@/a2ui/components";

function html(packId: string) {
  const surface = applyMessage(new Map(), {
    version: "v1.0",
    createSurface: {
      surfaceId: "pack-setup",
      catalogId: WORK_CATALOG_ID,
      dataModel: {},
      components: [
        { id: "root", component: "Column", children: ["setup"] },
        { id: "setup", component: "PackSetup", packId },
      ],
    },
  }).get("pack-setup")!;
  return renderToStaticMarkup(
    renderComponent(surface.components.get("root")!, { surface, extras: {} }) as ReactElement,
  );
}

describe("PackSetup card", () => {
  it("renders the shared pack setup card for a pack ID", () => {
    const out = html("magento");
    expect(out).toContain('aria-label="Pack setup"');
    expect(out).toContain("Loading pack configuration");
  });

  it("renders nothing for an ID that is not a pack ID", () => {
    expect(html("../magento")).not.toContain("Pack setup");
    expect(html("")).not.toContain("Pack setup");
  });

  it("tells the agent the pack is applied, with no configuration values", () => {
    const followUp = buildActionFollowUp(packAppliedFollowUp({ packId: "magento", name: "Magento", operation: "configure", status: "installed" }));
    expect(followUp).toContain("I applied the Magento pack in the card.");
    expect(followUp).toContain("run one small sample query");
    expect(followUp).toContain('"operation": "configure"');
    expect(followUp).not.toMatch(/password|secret|host/i);
  });

  it("asks the agent what to finish when the pack is not installed yet", () => {
    const followUp = buildActionFollowUp(packAppliedFollowUp({ packId: "shopify", name: "Shopify", operation: "install", status: "failed" }));
    expect(followUp).toContain('reports "failed"');
  });
});
