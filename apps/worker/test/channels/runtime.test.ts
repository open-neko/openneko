import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import { writeSecretsStore } from "@neko/secrets";
import { enabledChannelIds, getChannelProviders } from "../../src/channels/runtime.js";

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
});
