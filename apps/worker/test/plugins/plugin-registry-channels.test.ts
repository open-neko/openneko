import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { RPC_PROTOCOL_VERSION, rpcOk, type RpcResponse } from "@open-neko/plugin-types";
import {
  mockActionAdapter,
  setDefaultActionAdapter,
} from "@neko/llm/workflows";
import { PluginRegistry } from "../../src/plugins/plugin-registry";
import type {
  PluginRuntime,
  PluginVmSpec,
} from "../../src/plugins/plugin-runtime";

const FAKE_INTEGRITY = "sha512-" + "a".repeat(86) + "==";
const CHANNEL_NAME = "@open-neko/channel-telegram";

const TELEGRAM_PROFILE = {
  modalities: ["text"],
  richMedia: { markdown: true, cards: false, charts: false, images: true, interactiveControls: true },
  interaction: { turnTaking: "async", canApproveInline: true, quickReplies: true },
  constraints: { maxOutboundChars: 4096, latencyClass: "interactive", attentionModel: "push" },
  fidelity: "summary",
};

interface RecordedRpc {
  pluginId: string;
  method: string;
  paramsJson: string;
  env?: Record<string, string>;
}

class FakeRuntime implements PluginRuntime {
  readonly starts: PluginVmSpec[] = [];
  readonly rpcs: RecordedRpc[] = [];
  private readonly running = new Set<string>();
  constructor(
    private readonly responses: Record<string, RpcResponse> = {},
  ) {}
  hasPlugin(id: string): boolean {
    return this.running.has(id);
  }
  async start(spec: PluginVmSpec): Promise<void> {
    this.starts.push(spec);
    this.running.add(spec.id);
  }
  async callRpc(
    pluginId: string,
    method: string,
    paramsJson: string,
    options?: { env?: Record<string, string> },
  ): Promise<RpcResponse> {
    this.rpcs.push({ pluginId, method, paramsJson, ...(options?.env ? { env: options.env } : {}) });
    const r = this.responses[method];
    if (!r) throw new Error(`FakeRuntime: no response for ${method}`);
    return r;
  }
  async stop(pluginId: string): Promise<void> {
    this.running.delete(pluginId);
  }
  async destroyAll(): Promise<void> {
    this.running.clear();
  }
}

function manifestWithChannelEntry() {
  return {
    schema: "https://open-neko.github.io/plugins/manifest.schema.json",
    plugins: [
      {
        name: CHANNEL_NAME,
        version: "0.1.0",
        integrity: FAKE_INTEGRITY,
        permissions: { network: ["api.telegram.org"], env: [] },
        capabilities: {
          channel: {
            providerLabel: "Telegram",
            profile: TELEGRAM_PROFILE,
            directions: ["outbound", "inbound"],
            ingress: "webhook",
          },
        },
      },
    ],
  };
}

function channelRegisterResponse(): RpcResponse {
  return rpcOk({
    protocol: RPC_PROTOCOL_VERSION,
    pluginName: CHANNEL_NAME,
    pluginVersion: "0.1.0",
    capabilities: {
      channel: {
        providerLabel: "Telegram",
        profile: TELEGRAM_PROFILE,
        directions: ["outbound", "inbound"],
        ingress: "webhook",
      },
    },
  });
}

describe("PluginRegistry — channel capability", () => {
  let repoRoot: string;
  let workRoot: string;
  let secretsConfigDir: string;
  let runnerPath: string;

  beforeEach(async () => {
    repoRoot = await mkdtemp(path.join(tmpdir(), "openneko-repo-"));
    workRoot = await mkdtemp(path.join(tmpdir(), "openneko-vmwork-"));
    secretsConfigDir = await mkdtemp(path.join(tmpdir(), "openneko-secrets-"));
    runnerPath = path.join(repoRoot, "fake-runner.js");
    await writeFile(runnerPath, "// runner\n", "utf8");
    setDefaultActionAdapter(mockActionAdapter);
  });
  afterEach(async () => {
    await rm(repoRoot, { recursive: true, force: true });
    await rm(workRoot, { recursive: true, force: true });
    await rm(secretsConfigDir, { recursive: true, force: true });
    setDefaultActionAdapter(mockActionAdapter);
  });

  function newRegistry(runtime: PluginRuntime) {
    return new PluginRegistry({
      repoRoot,
      workRoot,
      secretsConfigDir,
      runtime,
      resolveRunner: () => runnerPath,
    });
  }

  async function writeManifest() {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify(manifestWithChannelEntry()),
      "utf8",
    );
  }

  it("getChannelProviders surfaces the installed channel + RegistryStatus.channels", async () => {
    await writeManifest();
    const reg = newRegistry(new FakeRuntime());
    await reg.start();
    const providers = reg.getChannelProviders();
    expect(providers).toHaveLength(1);
    expect(providers[0]).toMatchObject({
      pluginName: CHANNEL_NAME,
      providerLabel: "Telegram",
      directions: ["outbound", "inbound"],
      ingress: "webhook",
    });
    expect(reg.status().channels).toEqual([
      { pluginId: "open-neko-channel-telegram", providerLabel: "Telegram" },
    ]);
    await reg.stop();
  });

  it("deliverOnChannel spawns the VM then RPCs deliver with recipient/events/profile + env", async () => {
    await writeManifest();
    await writeFile(
      path.join(secretsConfigDir, "secrets.json"),
      JSON.stringify({ [CHANNEL_NAME]: { TELEGRAM_BOT_TOKEN: "tok-1" } }),
      "utf8",
    );
    const runtime = new FakeRuntime({
      register: channelRegisterResponse(),
      deliver: rpcOk({ delivered: true, ref: "278" }),
    });
    const reg = newRegistry(runtime);
    await reg.start();
    const res = await reg.deliverOnChannel(
      CHANNEL_NAME,
      { kind: "telegram", chatId: 555 },
      [{ kind: "inform", id: "i1", mood: "good", title: "T", body: "B" }],
    );
    expect(res).toEqual({ delivered: true, ref: "278" });
    const deliverRpc = runtime.rpcs.find((r) => r.method === "deliver");
    expect(deliverRpc).toBeTruthy();
    const params = JSON.parse(deliverRpc!.paramsJson) as {
      recipient: { chatId: number };
      events: unknown[];
      profile: { fidelity: string };
    };
    expect(params.recipient.chatId).toBe(555);
    expect(params.events).toHaveLength(1);
    expect(params.profile.fidelity).toBe("summary");
    expect(deliverRpc!.env).toEqual({ TELEGRAM_BOT_TOKEN: "tok-1" });
    await reg.stop();
  });

  it("parseInbound returns the intents + sender recipient the VM parsed", async () => {
    await writeManifest();
    const runtime = new FakeRuntime({
      register: channelRegisterResponse(),
      parse_inbound: rpcOk({
        intents: [{ kind: "decision", decisionRef: "ar-1", choice: "approve" }],
        recipient: { kind: "telegram", chatId: 42 },
      }),
    });
    const reg = newRegistry(runtime);
    await reg.start();
    const { intents, recipient } = await reg.parseInbound(CHANNEL_NAME, {
      callback_query: { data: "approve:ar-1" },
    });
    expect(intents).toEqual([{ kind: "decision", decisionRef: "ar-1", choice: "approve" }]);
    expect(recipient).toEqual({ kind: "telegram", chatId: 42 });
    await reg.stop();
  });

  it("pollInbound returns the VM's update batch + advanced cursor", async () => {
    await writeManifest();
    const runtime = new FakeRuntime({
      register: channelRegisterResponse(),
      poll_inbound: rpcOk({ updates: [{ update_id: 5 }], cursor: "6" }),
    });
    const reg = newRegistry(runtime);
    await reg.start();
    const { updates, cursor } = await reg.pollInbound(CHANNEL_NAME, "3");
    expect(updates).toEqual([{ update_id: 5 }]);
    expect(cursor).toBe("6");
    await reg.stop();
  });

  it("verifyInbound returns the VM's verdict", async () => {
    await writeManifest();
    const runtime = new FakeRuntime({
      register: channelRegisterResponse(),
      verify_inbound: rpcOk({ ok: true }),
    });
    const reg = newRegistry(runtime);
    await reg.start();
    expect(await reg.verifyInbound(CHANNEL_NAME, { "x-telegram-bot-api-secret-token": "s" }, "{}")).toBe(true);
    await reg.stop();
  });

  it("getPluginEnv merges manifest + secrets for the channel plugin", async () => {
    await writeManifest();
    await writeFile(
      path.join(secretsConfigDir, "secrets.json"),
      JSON.stringify({ [CHANNEL_NAME]: { TELEGRAM_BOT_TOKEN: "tok-xyz" } }),
      "utf8",
    );
    const reg = newRegistry(new FakeRuntime());
    await reg.start();
    expect(reg.getPluginEnv(CHANNEL_NAME)).toEqual({ TELEGRAM_BOT_TOKEN: "tok-xyz" });
    expect(reg.getPluginEnv("@open-neko/not-installed")).toBeNull();
    await reg.stop();
  });

  it("deliverOnChannel on a not-installed plugin errors", async () => {
    const reg = newRegistry(new FakeRuntime());
    await reg.start();
    await expect(
      reg.deliverOnChannel("@open-neko/channel-missing", { kind: "x" }, []),
    ).rejects.toThrow(/not installed/);
    await reg.stop();
  });

  it("deliverOnChannel on an installed non-channel plugin errors", async () => {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify({
        schema: "https://open-neko.github.io/plugins/manifest.schema.json",
        plugins: [
          {
            name: "@open-neko/plugin-slack",
            version: "0.1.0",
            integrity: FAKE_INTEGRITY,
            permissions: { network: ["slack.com"], env: [] },
            capabilities: { action: { kinds: [{ kind: "send_slack_message", description: "post" }] } },
          },
        ],
      }),
      "utf8",
    );
    const reg = newRegistry(new FakeRuntime());
    await reg.start();
    await expect(
      reg.deliverOnChannel("@open-neko/plugin-slack", { kind: "x" }, []),
    ).rejects.toThrow(/does not declare a channel/);
    await reg.stop();
  });
});
