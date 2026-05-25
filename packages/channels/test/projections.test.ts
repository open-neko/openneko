import { describe, expect, it } from "vitest";
import {
  EMAIL_DIGEST_PROFILE,
  SLACK_PROFILE,
  VOICE_PROFILE,
  WEB_PROFILE,
  WHATSAPP_PROFILE,
  type InteractionEvent,
} from "@neko/interaction";
import {
  slackProjection,
  voiceProjection,
  webProjection,
  whatsappProjection,
  type SlackBlock,
} from "../src/index.js";

const inform: InteractionEvent = {
  kind: "inform",
  id: "o1",
  mood: "good",
  title: "Q3 revenue landed",
  body: "Revenue hit $4.7M, up 12% MoM. Strong enterprise pull this quarter.",
  metric: { label: "Revenue MTD", value: "$4.7M" },
  series: { kind: "line", points: [{ d: "Mon", v: 1 }, { d: "Tue", v: 2 }] },
};

const ask: InteractionEvent = {
  kind: "ask",
  id: "a1",
  ask: "approval",
  prompt: "Approve $5k refund to ACME?",
  decisionRef: "ar-123",
  risk: "medium",
};

const blockTypes = (blocks: SlackBlock[]): string[] => blocks.map((b) => String(b.type));

describe("one inform, every substrate (degradation by profile)", () => {
  it("web keeps the full card with chart data", () => {
    const { surfaces, pendingAsks } = webProjection([inform], WEB_PROFILE);
    const update = surfaces[1] as { updateComponents: { components: Array<Record<string, unknown>> } };
    const card = update.updateComponents.components[0]!;
    expect(card.component).toBe("BriefingCard");
    expect(card.metric).toBe("$4.7M");
    expect((card.chartData as unknown[]).length).toBe(2);
    expect(pendingAsks).toHaveLength(0);
  });

  it("slack keeps the card but drops the chart to a note", () => {
    const { blocks } = slackProjection([inform], SLACK_PROFILE);
    const section = blocks[0] as { text: { text: string } };
    expect(section.text.text).toContain("Q3 revenue landed");
    expect(blockTypes(blocks)).toContain("context");
    expect(JSON.stringify(blocks)).toContain("Chart available");
    expect(JSON.stringify(blocks)).not.toContain("Tue");
  });

  it("whatsapp collapses to clamped text with the metric inline", () => {
    const { body } = whatsappProjection([inform], WHATSAPP_PROFILE);
    expect(body).toContain("*Q3 revenue landed*");
    expect(body).toContain("Revenue MTD: $4.7M");
    expect(body.length).toBeLessThanOrEqual(WHATSAPP_PROFILE.constraints.maxOutboundChars!);
    expect(body).not.toContain("Mon");
  });

  it("voice speaks the headline and metric, no chart", () => {
    const { ssml } = voiceProjection([inform], VOICE_PROFILE);
    expect(ssml.startsWith("<speak>")).toBe(true);
    expect(ssml).toContain("Q3 revenue landed");
    expect(ssml).toContain("Revenue MTD is $4.7M");
    expect(ssml).not.toContain("Tue");
  });
});

describe("ask negotiation branches on profile, never on channel identity", () => {
  it("same slack projection yields buttons under an inline-capable profile", () => {
    const { blocks } = slackProjection([ask], SLACK_PROFILE);
    expect(blockTypes(blocks)).toContain("actions");
    expect(JSON.stringify(blocks)).toContain('"action_id":"approve"');
  });

  it("and yields a link-back under a read-only profile — same function", () => {
    const { blocks } = slackProjection([ask], EMAIL_DIGEST_PROFILE);
    expect(blockTypes(blocks)).not.toContain("actions");
    expect(JSON.stringify(blocks)).toContain("Open the web dashboard");
  });

  it("whatsapp renders quick-reply buttons", () => {
    const { buttons } = whatsappProjection([ask], WHATSAPP_PROFILE);
    expect(buttons).toEqual([
      { id: "approve:ar-123", title: "Approve" },
      { id: "reject:ar-123", title: "Reject" },
    ]);
  });

  it("web surfaces the ask to its native approval UI", () => {
    const { pendingAsks } = webProjection([ask], WEB_PROFILE);
    expect(pendingAsks).toHaveLength(1);
    expect(pendingAsks[0]!.decisionRef).toBe("ar-123");
  });

  it("voice speaks the ask as a yes/no", () => {
    const { ssml } = voiceProjection([ask], VOICE_PROFILE);
    expect(ssml).toContain("Say approve or reject");
  });
});
