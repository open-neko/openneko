// Built-in channel providers for the worker's admin item list. A channel is
// on while every required setting has a value in the secrets store under
// `channel.<id>`. openneko web owns sends, inbound and the settings.
import {
  BUILT_IN_CHANNELS,
  BUILT_IN_CHANNEL_IDS,
  channelSecretSection,
  type BuiltInChannelId,
  type ChannelEnv,
  type ChannelSubstrate,
} from "@neko/channels";
import { readSecretsStore, type SecretsStore } from "@neko/secrets";

export interface ChannelProviderInfo {
  id: BuiltInChannelId;
  providerLabel: string;
  directions: string[];
  ingress: string;
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
