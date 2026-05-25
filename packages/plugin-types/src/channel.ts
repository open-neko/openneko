import { z } from "zod";

/**
 * Channel capability — a plugin is a frontend (Slack, WhatsApp, voice, …).
 *
 * Non-singleton like `connect`: any number of installed plugins may declare it,
 * and an operator can run several at once. The worker projects the agent's
 * modality-free InteractionEvents into the substrate's native payload inside
 * the plugin VM (`deliver`), and normalizes inbound substrate payloads back
 * into IntentEvents (`parse_inbound`), verifying webhook signatures in-VM
 * (`verify_inbound`).
 */

export const Modality = z.enum(["text", "visual", "voice", "haptic", "neural"]);
export const TurnTaking = z.enum(["async", "streaming", "realtime"]);
export const LatencyClass = z.enum(["batch", "interactive", "realtime"]);
export const AttentionModel = z.enum(["pull", "push"]);
export const Fidelity = z.enum(["headline", "summary", "full"]);

/**
 * What a substrate can carry. A projection may branch ONLY on this — never on
 * the channel's identity. Additive-only: new fields default such that existing
 * channels are unaffected (the "capabilities, not switch statements" rule).
 */
export const CapabilityProfile = z.object({
  modalities: z.array(Modality).min(1),
  richMedia: z.object({
    markdown: z.boolean(),
    cards: z.boolean(),
    charts: z.boolean(),
    images: z.boolean(),
    interactiveControls: z.boolean(),
  }),
  interaction: z.object({
    turnTaking: TurnTaking,
    canApproveInline: z.boolean(),
    quickReplies: z.boolean(),
  }),
  constraints: z.object({
    maxOutboundChars: z.number().int().positive().optional(),
    latencyClass: LatencyClass,
    attentionModel: AttentionModel,
  }),
  fidelity: Fidelity,
});
export type CapabilityProfile = z.infer<typeof CapabilityProfile>;

export const ChannelDirection = z.enum(["inbound", "outbound"]);
export type ChannelDirection = z.infer<typeof ChannelDirection>;

export const ChannelIngress = z.enum(["webhook", "socket", "none"]);
export type ChannelIngress = z.infer<typeof ChannelIngress>;

export const ChannelCapabilityDeclaration = z.object({
  providerLabel: z.string().min(1),
  profile: CapabilityProfile,
  directions: z.array(ChannelDirection).min(1),
  ingress: ChannelIngress.default("none"),
});
export type ChannelCapabilityDeclaration = z.infer<typeof ChannelCapabilityDeclaration>;

/** Opaque to the worker; minted at connect/config time and stored in a delivery binding. */
export const ChannelRecipient = z
  .object({ kind: z.string().min(1) })
  .catchall(z.unknown());
export type ChannelRecipient = z.infer<typeof ChannelRecipient>;

/*
 * Protocol payloads. The concrete event shapes are the InteractionEvent /
 * IntentEvent unions in @neko/interaction; they are carried opaquely across the
 * published wire here — the protocol stays internal while the RPC + profile
 * types are published. A channel author gets typed events by importing the
 * protocol package directly.
 */
export const DeliverParams = z.object({
  recipient: ChannelRecipient,
  events: z.array(z.unknown()),
  profile: CapabilityProfile,
});
export type DeliverParams = z.infer<typeof DeliverParams>;

export const DeliverResult = z.object({
  delivered: z.boolean(),
  ref: z.string().optional(),
});
export type DeliverResult = z.infer<typeof DeliverResult>;

export const ParseInboundParams = z.object({ raw: z.unknown() });
export type ParseInboundParams = z.infer<typeof ParseInboundParams>;

export const ParseInboundResult = z.object({
  intents: z.array(z.unknown()),
  // Sender's channel-native address (the chat that messaged us). Lets the worker
  // auto-create a delivery binding on first contact, so operators never hand-write
  // one. Optional: outbound-only or anonymous inbound omit it.
  recipient: ChannelRecipient.optional(),
});
export type ParseInboundResult = z.infer<typeof ParseInboundResult>;

// poll_inbound — the provider-agnostic pull transport. The worker loops this when
// no public webhook URL is configured (local/dev, no-ingress hosts); the plugin
// fetches its native update batch and returns the updates already split, each fed
// back through parse_inbound. `cursor` is an opaque continuation token the worker
// echoes on the next call.
export const PollInboundParams = z.object({ cursor: z.string().optional() });
export type PollInboundParams = z.infer<typeof PollInboundParams>;

export const PollInboundResult = z.object({
  updates: z.array(z.unknown()),
  cursor: z.string().optional(),
});
export type PollInboundResult = z.infer<typeof PollInboundResult>;

export const VerifyInboundParams = z.object({
  headers: z.record(z.string(), z.string()),
  body: z.string(),
});
export type VerifyInboundParams = z.infer<typeof VerifyInboundParams>;

export const VerifyInboundResult = z.object({ ok: z.boolean() });
export type VerifyInboundResult = z.infer<typeof VerifyInboundResult>;
