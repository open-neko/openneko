import {
  SLACK_PROFILE,
  VOICE_PROFILE,
  WEB_PROFILE,
  WHATSAPP_PROFILE,
} from "@neko/interaction";
import { defineChannel, type ChannelAdapter, type DeliverResult } from "./channel-adapter";
import { parseSlackInbound } from "./inbound/slack";
import { parseWhatsappInbound } from "./inbound/whatsapp";
import { slackProjection } from "./projections/slack";
import { voiceProjection } from "./projections/voice";
import { webProjection } from "./projections/web";
import { whatsappProjection } from "./projections/whatsapp";

const localSend = async (): Promise<DeliverResult> => ({ delivered: true, ref: "local" });

export const createWebChannel = (): ChannelAdapter =>
  defineChannel({
    pluginName: "web",
    providerLabel: "Web",
    profile: WEB_PROFILE,
    directions: ["inbound", "outbound"],
    builtIn: true,
    project: webProjection,
    send: localSend,
  });

export const createSlackChannel = (): ChannelAdapter =>
  defineChannel({
    pluginName: "@open-neko/plugin-slack",
    providerLabel: "Slack",
    profile: SLACK_PROFILE,
    directions: ["inbound", "outbound"],
    project: slackProjection,
    send: localSend,
    parseInbound: parseSlackInbound,
  });

export const createWhatsappChannel = (): ChannelAdapter =>
  defineChannel({
    pluginName: "@open-neko/channel-whatsapp",
    providerLabel: "WhatsApp",
    profile: WHATSAPP_PROFILE,
    directions: ["inbound", "outbound"],
    project: whatsappProjection,
    send: localSend,
    parseInbound: parseWhatsappInbound,
  });

export const createVoiceChannel = (): ChannelAdapter =>
  defineChannel({
    pluginName: "@open-neko/channel-voice",
    providerLabel: "Voice",
    profile: VOICE_PROFILE,
    directions: ["inbound", "outbound"],
    project: voiceProjection,
    send: localSend,
  });
