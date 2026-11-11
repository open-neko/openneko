import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import { readSecretsStore, writeSecretsStore } from "@neko/secrets";
import {
  channelEnv,
  channelSettings,
  deliverOnChannel,
  enabledChannelIds,
  getChannelProviders,
  migrateLegacyChannelSecrets,
  setChannelSettings,
  setChannelState,
  verifyChannelInbound,
} from "../../src/channels/runtime.js";

let root: string;

beforeAll(async () => {
  root = await mkdtemp(join(tmpdir(), "channel-runtime-"));
  vi.stubEnv("XDG_CONFIG_HOME", root);
});

afterAll(async () => {
  vi.unstubAllEnvs();
  await rm(root, { recursive: true, force: true });
});

beforeEach(async () => {
  await writeSecretsStore({});
});

describe("enabled channels", () => {
  it("turns on each channel whose required settings are stored", async () => {
    await writeSecretsStore({
      "channel.slack": { SLACK_BOT_TOKEN: "xoxb-1" },
      "channel.telegram": { TELEGRAM_BOT_TOKEN: "123:abc" },
      "channel.hud": { HUD_CHANNEL_WORKSPACE: "demo" },
    });
    expect(await enabledChannelIds()).toEqual(["slack", "telegram"]);
    expect((await getChannelProviders()).map((p) => [p.id, p.ingress])).toEqual([
      ["slack", "socket"],
      ["telegram", "webhook"],
    ]);
  });

  it("keeps a channel off while a required setting is empty", async () => {
    await writeSecretsStore({ "channel.slack": { SLACK_BOT_TOKEN: "  ", SLACK_APP_TOKEN: "xapp-1" } });
    expect(await enabledChannelIds()).toEqual([]);
  });

  it("refuses delivery and webhooks for a channel that is off", async () => {
    await expect(deliverOnChannel("telegram", { chatId: 1 }, [])).rejects.toThrow(/not turned on/);
    expect(await verifyChannelInbound("telegram", {}, "{}")).toBe(false);
  });
});

describe("upgrade from the channel plugins", () => {
  it("turns Slack on when its credentials moved from the plugin", async () => {
    await writeSecretsStore({
      "@open-neko/plugin-slack": { SLACK_BOT_TOKEN: "xoxb-old", SLACK_APP_TOKEN: "xapp-old" },
    });
    await migrateLegacyChannelSecrets();
    expect(await enabledChannelIds()).toEqual(["slack"]);
    expect((await channelSettings()).find((c) => c.id === "slack")).toMatchObject({ enabled: true, missing: [] });
  });

  it("leaves every channel off when no credentials moved", async () => {
    await writeSecretsStore({ "pack.magento": { KEY: "keep" } });
    expect(await migrateLegacyChannelSecrets()).toEqual([]);
    expect(await enabledChannelIds()).toEqual([]);
    expect((await channelSettings()).map((c) => c.enabled)).toEqual([false, false, false]);
  });
});

describe("channel settings", () => {
  it("writes declared settings, turns the channel on and reports secrets only as set", async () => {
    await setChannelSettings("telegram", { TELEGRAM_BOT_TOKEN: "123:abc" });
    expect(await channelEnv("telegram")).toEqual({ TELEGRAM_BOT_TOKEN: "123:abc" });
    const telegram = (await channelSettings()).find((c) => c.id === "telegram")!;
    expect(telegram.enabled).toBe(true);
    expect(telegram.missing).toEqual([]);
    expect(telegram.fields.find((f) => f.key === "TELEGRAM_BOT_TOKEN")).toMatchObject({ set: true });
    expect(JSON.stringify(telegram)).not.toContain("123:abc");
  });

  it("refuses undeclared and generated keys; clearing a required one turns the channel off", async () => {
    await expect(setChannelSettings("slack", { NOPE: "x" })).rejects.toThrow(/does not declare/);
    await expect(setChannelSettings("hud", { HUD_CHANNEL_SECRET: "mine" })).rejects.toThrow(/does not declare/);
    await setChannelSettings("slack", { SLACK_BOT_TOKEN: "xoxb-1" });
    expect(await enabledChannelIds()).toEqual(["slack"]);
    await setChannelSettings("slack", { SLACK_BOT_TOKEN: null });
    expect(await enabledChannelIds()).toEqual([]);
  });
});

describe("setChannelState", () => {
  it("turns the HUD on by minting its secret once, with no pack", async () => {
    expect(await enabledChannelIds()).toEqual([]);
    await setChannelState("hud", "on");
    const env = await channelEnv("hud");
    expect(env.HUD_CHANNEL_SECRET).toMatch(/^[A-Za-z0-9_-]{64}$/);
    expect(await enabledChannelIds()).toEqual(["hud"]);
    await setChannelState("hud", "on");
    expect((await channelEnv("hud")).HUD_CHANNEL_SECRET).toBe(env.HUD_CHANNEL_SECRET);
  });

  it("refuses to turn on a channel that lacks an admin-entered setting", async () => {
    await expect(setChannelState("slack", "on")).rejects.toThrow(/needs SLACK_BOT_TOKEN/);
    expect(await enabledChannelIds()).toEqual([]);
  });

  it("turns a channel off by removing its stored settings and leaves others alone", async () => {
    await writeSecretsStore({
      "channel.slack": { SLACK_BOT_TOKEN: "xoxb-1", SLACK_APP_TOKEN: "xapp-1" },
      "channel.telegram": { TELEGRAM_BOT_TOKEN: "123:abc" },
    });
    await setChannelState("slack", "off");
    const store = await readSecretsStore();
    expect(store["channel.slack"]).toBeUndefined();
    expect(await enabledChannelIds()).toEqual(["telegram"]);
  });
});

describe("migrateLegacyChannelSecrets", () => {
  it("moves plugin-era sections to channel sections without overwriting newer values", async () => {
    await writeSecretsStore({
      "@open-neko/plugin-slack": { SLACK_BOT_TOKEN: "xoxb-old", SLACK_APP_TOKEN: "xapp-old" },
      "@open-neko/channel-telegram": { TELEGRAM_BOT_TOKEN: "tg-old" },
      "channel.telegram": { TELEGRAM_BOT_TOKEN: "tg-new" },
      "pack.magento": { KEY: "keep" },
    });
    expect(await migrateLegacyChannelSecrets()).toEqual(["@open-neko/plugin-slack", "@open-neko/channel-telegram"]);
    const store = await readSecretsStore();
    expect(store["channel.slack"]).toEqual({ SLACK_BOT_TOKEN: "xoxb-old", SLACK_APP_TOKEN: "xapp-old" });
    expect(store["channel.telegram"]).toEqual({ TELEGRAM_BOT_TOKEN: "tg-new" });
    expect(store["@open-neko/plugin-slack"]).toBeUndefined();
    expect(store["pack.magento"]).toEqual({ KEY: "keep" });
    expect(await migrateLegacyChannelSecrets()).toEqual([]);
  });
});
