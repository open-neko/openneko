import { describe, expect, it } from "vitest";
import type { ReactElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { applyMessage, getResolvedComponents } from "@/a2ui/surface";
import { renderComponent } from "@/a2ui/renderer";
import "@/a2ui/components"; // side-effect: registers the real Briefing/Markdown/BriefingCard renderers
import { CATALOG_ID, ComponentTypes } from "@/a2ui/catalog";
import type { A2UIComponent, A2UIMessage, SurfaceState } from "@/a2ui/types";
import { webProjection } from "@neko/channels";
import { WEB_PROFILE, type IntentEvent } from "@neko/interaction";
import type { AgentEvent } from "@neko/llm";
import { toInteractionEvents } from "@neko/llm/interaction";

/**
 * End-to-end through the web channel, all real code, no plugins/server/DB:
 *   AgentEvent[]  →  toInteractionEvents (the waist mapper)
 *                 →  webProjection (the built-in web channel)
 *                 →  applyMessage (apps/web's actual A2UI surface reducer)
 *                 →  resolved components the renderer consumes.
 */

const agentStream: AgentEvent[] = [
  { type: "status", message: "Reading the sales data source" },
  {
    type: "surface",
    messages: [
      { version: "v0.9", createSurface: { surfaceId: "x", catalogId: CATALOG_ID } },
      {
        version: "v0.9",
        updateComponents: {
          surfaceId: "x",
          components: [
            {
              id: "card1",
              component: "BriefingCard",
              mood: "good",
              text: "Q3 revenue landed at $4.7M",
              metric: "$4.7M",
              label: "Revenue MTD",
              detail: "Up 12% MoM, driven by enterprise expansion.",
              chartType: "line",
              chartData: [{ d: "Jul", v: 3.9 }, { d: "Aug", v: 4.2 }, { d: "Sep", v: 4.7 }],
            },
          ],
        },
      },
    ],
  },
  { type: "message", role: "assistant", content: "Want me to post the Q3 summary to #exec?" },
  {
    type: "action_request_emit",
    action_request_id: "ar-501",
    kind: "send_slack_message",
    scope: "external",
    risk_level: "medium",
    intent: "Post the Q3 revenue summary to #exec",
    decision: "pending_approval",
  },
];

const buildSurface = () => {
  const events = toInteractionEvents(agentStream);
  const projection = webProjection(events, WEB_PROFILE);
  let surfaces = new Map<string, SurfaceState>();
  for (const message of projection.surfaces) {
    surfaces = applyMessage(surfaces, message as unknown as A2UIMessage);
  }
  return { events, projection, surfaces };
};

describe("web channel — end to end through the real A2UI surface pipeline", () => {
  it("targets the real briefing catalog", () => {
    const { projection } = buildSurface();
    const create = projection.surfaces[0] as unknown as { createSurface: { catalogId: string } };
    expect(create.createSurface.catalogId).toBe(CATALOG_ID);
  });

  it("renders a BriefingCard the web reducer resolves, carrying the agent's numbers", () => {
    const { surfaces } = buildSurface();
    const surface = surfaces.get("s1");
    expect(surface).toBeDefined();

    const resolved = getResolvedComponents(surface!);
    const card = resolved.find((c) => c.component === ComponentTypes.BriefingCard) as A2UIComponent;
    expect(card).toMatchObject({
      mood: "good",
      text: "Q3 revenue landed at $4.7M",
      metric: "$4.7M",
      label: "Revenue MTD",
      chartType: "line",
    });
    expect((card.chartData as unknown[]).length).toBe(3);
  });

  it("renders the assistant reply as Markdown", () => {
    const { surfaces } = buildSurface();
    const resolved = getResolvedComponents(surfaces.get("s1")!);
    const markdown = resolved.find((c) => c.component === ComponentTypes.Markdown) as A2UIComponent;
    expect(markdown.text).toBe("Want me to post the Q3 summary to #exec?");
  });

  it("surfaces the approval to the web's native affordance, not A2UI", () => {
    const { projection, surfaces } = buildSurface();
    const resolved = getResolvedComponents(surfaces.get("s1")!);
    // the ask is NOT an A2UI component — it's a web-native pending approval
    expect(resolved.some((c) => c.component === "ApprovalRequest")).toBe(false);
    expect(projection.pendingAsks).toHaveLength(1);
    expect(projection.pendingAsks[0]!.decisionRef).toBe("ar-501");
  });

  it("closes the loop: a web Approve click yields a decision for the same request", () => {
    const { projection } = buildSurface();
    const ask = projection.pendingAsks[0]!;
    // what the web Approve button emits → feeds approveActionRequest(orgId, decisionRef)
    const intent: IntentEvent = { kind: "decision", decisionRef: ask.decisionRef, choice: "approve" };
    expect(intent).toEqual({ kind: "decision", decisionRef: "ar-501", choice: "approve" });
  });
});

const renderHtml = (component: A2UIComponent, surface: SurfaceState): string =>
  renderToStaticMarkup(renderComponent(component, { surface, extras: {} }) as ReactElement);

describe("web channel — rendered HTML through the real React renderers", () => {
  it("the BriefingCard renders the agent's metric and label", () => {
    const { surfaces } = buildSurface();
    const surface = surfaces.get("s1")!;
    const card = getResolvedComponents(surface).find((c) => c.component === ComponentTypes.BriefingCard)!;
    const html = renderHtml(card, surface);
    expect(html).toContain("$4.7M");
    expect(html).toContain("Revenue MTD");
  });

  it("the assistant reply renders as markdown HTML", () => {
    const { surfaces } = buildSurface();
    const surface = surfaces.get("s1")!;
    const markdown = getResolvedComponents(surface).find((c) => c.component === ComponentTypes.Markdown)!;
    const html = renderHtml(markdown, surface);
    expect(html).toContain("Want me to post the Q3 summary to #exec?");
  });
});
