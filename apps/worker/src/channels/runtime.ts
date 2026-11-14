// Built-in channel runtime. Slack, Telegram and the HUD channel ship in
// @neko/channels and run in-process. A channel is on while every required
// setting has a value in the secrets store under `channel.<id>`.
import { randomBytes } from "node:crypto";
import {
  BUILT_IN_CHANNELS,
  BUILT_IN_CHANNEL_IDS,
  LEGACY_CHANNEL_NAMES,
  builtInChannel,
  channelSecretSection,
  isBuiltInChannelId,
  type BuiltInChannelId,
  type ChannelEnv,
  type ChannelSubstrate,
  type ParsedInbound,
} from "@neko/channels";
import {
  readSecretsStore,
  writeSecretsStore,
  type SecretsStore,
} from "@neko/secrets";

export interface ChannelProviderInfo {
  id: BuiltInChannelId;
  providerLabel: string;
  directions: string[];
  ingress: string;
}

export interface ChannelSettingsView extends ChannelProviderInfo {
  enabled: boolean;
  fields: Array<{ key: string; required: boolean; secret: boolean; description: string; set: boolean; value?: string }>;
  missing: string[];
}

export class ChannelSettingsError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ChannelSettingsError";
  }
}

function requireChannel(id: string): ChannelSubstrate {
  const channel = builtInChannel(id);
  if (!channel) throw new ChannelSettingsError(`unknown channel ${id}`);
  return channel;
}

const providerInfo = (channel: ChannelSubstrate): ChannelProviderInfo => ({
  id: channel.id as BuiltInChannelId,
  providerLabel: channel.providerLabel,
  directions: [...channel.directions],
  ingress: channel.ingress,
});

function hasRequiredSettings(channel: ChannelSubstrate, env: ChannelEnv): boolean {
  return channel.settings.every((setting) => !setting.required || Boolean(env[setting.key]?.trim()));
}

/** Channels whose required settings are all stored, in declaration order. */
export async function enabledChannelIds(store?: SecretsStore): Promise<BuiltInChannelId[]> {
  const secrets = store ?? (await readSecretsStore());
  return BUILT_IN_CHANNEL_IDS.filter((id) =>
    hasRequiredSettings(BUILT_IN_CHANNELS[id], secrets[channelSecretSection(id)] ?? {}),
  );
}

export async function getChannelProviders(): Promise<ChannelProviderInfo[]> {
  return (await enabledChannelIds()).map((id) => providerInfo(BUILT_IN_CHANNELS[id]));
}

/** A channel's credentials and settings from the secrets store. */
export async function channelEnv(id: string, store?: SecretsStore): Promise<ChannelEnv> {
  const channel = requireChannel(id);
  const secrets = store ?? (await readSecretsStore());
  return { ...(secrets[channelSecretSection(channel.id as BuiltInChannelId)] ?? {}) };
}

export async function parseChannelInbound(id: string, raw: unknown): Promise<ParsedInbound> {
  const channel = requireChannel(id);
  return channel.parseInbound(raw, await channelEnv(channel.id));
}

export async function verifyChannelInbound(
  id: string,
  headers: Record<string, string>,
  body: string,
): Promise<boolean> {
  const channel = builtInChannel(id);
  if (!channel?.verifyInbound) return false;
  if (!(await enabledChannelIds()).includes(channel.id as BuiltInChannelId)) return false;
  return channel.verifyInbound(headers, body, await channelEnv(channel.id));
}

export async function pollChannelInbound(
  id: string,
  cursor?: string,
): Promise<{ updates: unknown[]; cursor?: string }> {
  const channel = requireChannel(id);
  if (!channel.pollInbound) throw new Error(`channel ${channel.id} does not implement poll_inbound`);
  return channel.pollInbound(cursor, await channelEnv(channel.id));
}

/** Declared settings of every built-in channel; secrets report only whether they are set. */
export async function channelSettings(): Promise<ChannelSettingsView[]> {
  const store = await readSecretsStore();
  const enabled = await enabledChannelIds(store);
  return BUILT_IN_CHANNEL_IDS.map((id) => {
    const channel = BUILT_IN_CHANNELS[id];
    const env = store[channelSecretSection(id)] ?? {};
    const fields = channel.settings
      .filter((setting) => !setting.autogenerate)
      .map((setting) => {
        const value = env[setting.key];
        const set = typeof value === "string" && value.length > 0;
        return {
          key: setting.key,
          required: setting.required,
          secret: setting.secret,
          description: setting.description,
          set,
          ...(set && !setting.secret ? { value } : {}),
        };
      });
    return {
      ...providerInfo(channel),
      enabled: enabled.includes(id),
      fields,
      missing: fields.filter((f) => f.required && !f.set).map((f) => f.key),
    };
  });
}

/** Writes or clears declared settings of one channel. Refuses undeclared keys. */
export async function setChannelSettings(id: string, values: Record<string, string | null>): Promise<void> {
  const channel = requireChannel(id);
  const declared = new Set(channel.settings.filter((s) => !s.autogenerate).map((s) => s.key));
  for (const key of Object.keys(values)) {
    if (!declared.has(key)) throw new ChannelSettingsError(`${channel.id} does not declare ${key}`);
  }
  const store = await readSecretsStore();
  const section = channelSecretSection(channel.id as BuiltInChannelId);
  const next = { ...(store[section] ?? {}) };
  for (const [key, value] of Object.entries(values)) {
    if (value === null) delete next[key];
    else next[key] = value;
  }
  await writeSecretsStore({ ...store, [section]: next });
}

/**
 * Turns a channel on by minting the secrets OpenNeko generates for it, or off
 * by removing every stored setting. Turning on refuses while an admin-entered
 * required setting is missing.
 */
export async function setChannelState(id: string, state: "on" | "off"): Promise<void> {
  const channel = requireChannel(id);
  const store = await readSecretsStore();
  const section = channelSecretSection(channel.id as BuiltInChannelId);
  if (state === "off") {
    if (!(section in store)) return;
    const next = { ...store };
    delete next[section];
    await writeSecretsStore(next);
    return;
  }
  const env = { ...(store[section] ?? {}) };
  const missing = channel.settings.filter((s) => s.required && !s.autogenerate && !env[s.key]?.trim());
  if (missing.length > 0) {
    throw new ChannelSettingsError(`${channel.id} needs ${missing.map((s) => s.key).join(", ")} before it can turn on`);
  }
  let changed = false;
  for (const setting of channel.settings) {
    if (setting.autogenerate && !env[setting.key]) {
      env[setting.key] = randomBytes(48).toString("base64url");
      changed = true;
    }
  }
  if (changed) await writeSecretsStore({ ...store, [section]: env });
}

/**
 * One-time move of channel credentials from the sections the channel plugins
 * used (`@open-neko/plugin-slack`, …) to `channel.<id>`. Values already in
 * the new section win; the old section is removed once copied.
 */
export async function migrateLegacyChannelSecrets(): Promise<string[]> {
  const store = await readSecretsStore();
  const moved: string[] = [];
  const next: SecretsStore = { ...store };
  for (const [legacy, id] of Object.entries(LEGACY_CHANNEL_NAMES)) {
    const old = next[legacy];
    if (!old) continue;
    const section = channelSecretSection(id);
    const keys = new Set(BUILT_IN_CHANNELS[id].settings.map((s) => s.key));
    const carried = Object.fromEntries(Object.entries(old).filter(([key]) => keys.has(key)));
    next[section] = { ...carried, ...(next[section] ?? {}) };
    delete next[legacy];
    moved.push(legacy);
  }
  if (moved.length > 0) await writeSecretsStore(next);
  return moved;
}

/** Hook the settings routes call after they change which channels are on. */
let channelsChanged: (() => void) | null = null;

export function setChannelsChangedHook(hook: (() => void) | null): void {
  channelsChanged = hook;
}

export function notifyChannelsChanged(): void {
  channelsChanged?.();
}
