import type {
  CapabilityProfile,
  InteractionEvent,
  IntentEvent,
  Projection,
} from "@neko/interaction";

export type Direction = "inbound" | "outbound";

/** Opaque to the worker; minted at connect/config time. */
export type ChannelRecipient = { kind: string; [key: string]: unknown };

export interface DeliverResult {
  delivered: boolean;
  ref?: string;
  error?: string;
}

export interface ChannelDeliveryOutcome {
  native: unknown;
  result: DeliverResult;
}

/**
 * A membrane, with its Native payload type erased. Built-in channels build one
 * in-process via `defineChannel`; plugin channels expose the same surface over
 * one-shot RPC. The worker never handles a typed intermediate payload — exactly
 * as it doesn't when `deliver` runs inside a plugin VM.
 */
export interface ChannelAdapter {
  pluginName: string;
  providerLabel: string;
  profile: CapabilityProfile;
  directions: Direction[];
  /** Built-in channels deliver to every audience without an explicit binding. */
  builtIn?: boolean;
  deliver: (recipient: ChannelRecipient, events: InteractionEvent[]) => Promise<ChannelDeliveryOutcome>;
  parseInbound?: (raw: unknown) => IntentEvent[];
}

export interface ChannelProviderInfo {
  pluginName: string;
  providerLabel: string;
  profile: CapabilityProfile;
  directions: Direction[];
  builtIn: boolean;
}

/** Authoring shape for an in-process channel: a pure projection + a send. */
export interface ChannelAdapterSpec<Native> {
  pluginName: string;
  providerLabel: string;
  profile: CapabilityProfile;
  directions: Direction[];
  builtIn?: boolean;
  project: Projection<Native>;
  send: (recipient: ChannelRecipient, native: Native) => Promise<DeliverResult>;
  parseInbound?: (raw: unknown) => IntentEvent[];
}

/** Closes project+send over the substrate's Native type, yielding an erased adapter. */
export const defineChannel = <Native>(spec: ChannelAdapterSpec<Native>): ChannelAdapter => ({
  pluginName: spec.pluginName,
  providerLabel: spec.providerLabel,
  profile: spec.profile,
  directions: spec.directions,
  builtIn: spec.builtIn,
  parseInbound: spec.parseInbound,
  deliver: async (recipient, events) => {
    const native = spec.project(events, spec.profile);
    const result = await spec.send(recipient, native);
    return { native, result };
  },
});
