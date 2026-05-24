import { describe, expect, it } from "vitest";
import type {
  SlackProjectionResult,
  VoiceProjectionResult,
  WhatsappProjectionResult,
} from "@neko/channels";
import { runMultiChannelDemo } from "../scripts/multi-channel-demo.js";

describe("multi-channel demo (one stream → every membrane → back)", () => {
  it("maps the agent stream to a modality-free waist", async () => {
    const { interactionEvents } = await runMultiChannelDemo();
    const kinds = interactionEvents.map((e) => e.kind);
    expect(kinds).toContain("inform");
    expect(kinds).toContain("converse");
    expect(kinds).toContain("ask");

    const inform = interactionEvents.find((e) => e.kind === "inform");
    expect(inform).toMatchObject({ metric: { value: "$4.7M" }, series: { kind: "line" } });

    const ask = interactionEvents.find((e) => e.kind === "ask");
    expect(ask).toMatchObject({ decisionRef: "ar-501", ask: "approval" });
  });

  it("fans one stream out to web + slack + whatsapp + voice", async () => {
    const { outbound } = await runMultiChannelDemo();
    const reached = outbound.deliveries.map((d) => d.channelPlugin).sort();
    expect(reached).toEqual([
      "@open-neko/channel-voice",
      "@open-neko/channel-whatsapp",
      "@open-neko/plugin-slack",
      "web",
    ]);
  });

  it("projects each substrate from the same core", async () => {
    const { outbound } = await runMultiChannelDemo();
    const native = (plugin: string) => outbound.deliveries.find((d) => d.channelPlugin === plugin)?.native;

    const slack = native("@open-neko/plugin-slack") as SlackProjectionResult;
    expect(JSON.stringify(slack.blocks)).toContain('"action_id":"approve"');
    expect(JSON.stringify(slack.blocks)).toContain("Chart available"); // chart dropped to a note

    const whatsapp = native("@open-neko/channel-whatsapp") as WhatsappProjectionResult;
    expect(whatsapp.body).toContain("Revenue MTD: $4.7M");
    expect(whatsapp.buttons).toContainEqual({ id: "approve:ar-501", title: "Approve" });

    const voice = native("@open-neko/channel-voice") as VoiceProjectionResult;
    expect(voice.ssml).toContain("Say approve or reject");
    expect(voice.ssml).not.toContain("Jul"); // no chart axis labels spoken
  });

  it("normalizes inbound taps and text back to the same agent entry points", async () => {
    const { inbound } = await runMultiChannelDemo();
    expect(inbound.slack).toEqual([{ kind: "decision", decisionRef: "ar-501", choice: "approve" }]);
    expect(inbound.whatsapp).toEqual([{ kind: "utterance", text: "what's our churn rate?" }]);
  });
});
