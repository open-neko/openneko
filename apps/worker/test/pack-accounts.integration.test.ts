import { mkdtemp, mkdir, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { stringify } from "yaml";
import { pool } from "@neko/db";
import { maybeDecryptSecret, maybeEncryptSecret, _resetSecretKeyCacheForTesting } from "@neko/secret-crypt";
import { PackService } from "../src/packs/service";

const state = vi.hoisted(() => ({ refreshes: 0, failRefresh: false, revokes: 0, failRevoke: false }));
vi.mock("../src/plugins/plugin-registry", () => { throw new Error("Pack accounts must not load plugins"); });
vi.mock("../src/packs/connector-runner", () => ({ runPackConnector: async (_connector: unknown, request?: { connection?: string; credential?: unknown; input: Record<string, any> }) => {
  if (!request) return;
  const input = request.input;
  if (request.connection === "authorize") {
    const url = new URL("https://provider.example/authorize");
    Object.entries({ client_id: input.client.clientId, scope: input.scopes.join(" "), state: input.state, redirect_uri: input.redirectUri, response_type: "code", code_challenge: input.codeChallenge, code_challenge_method: "S256" }).forEach(([key, value]) => url.searchParams.set(key, value));
    return { authorizationUrl: url.toString() };
  }
  if (request.connection === "exchange") return { accountId: input.code, label: `${input.code}@example.test`, scopes: input.code === "partial" ? [] : input.scopes, expiresAt: Date.now() + 3600_000, tokens: { access: `private-${input.code}`, refresh: "rotation-0" } };
  if (request.connection === "refresh") {
    state.refreshes++;
    if (state.failRefresh) throw new Error("provider refresh failed");
    return { ...input.credential, expiresAt: Date.now() + 3600_000, tokens: { refresh: `rotation-${state.refreshes}` } };
  }
  if (request.connection === "revoke") { state.revokes++; return { revoked: !state.failRevoke }; }
  return { accountId: (request.credential as { accountId?: string })?.accountId };
} }));

describe.skipIf(process.env.OPENNEKO_PACK_ACCOUNTS_TEST !== "1")("pack accounts with PostgreSQL", () => {
  const org = `pack-accounts-${Date.now()}`;
  let root: string;
  let service: PackService;
  let first: string;
  let second: string;
  const manifest = {
    apiVersion: "openneko.app/v1", kind: "SolutionPack",
    metadata: { id: "account-fixture", name: "Account fixture", version: "1.0.0", publisher: "fixture", category: "operations" },
    compatibility: { openneko: ">=2.40.0", applications: [], databases: [] }, inputs: [], secrets: [], artifacts: { skills: [] },
    health: { requiredPreflight: [], postInstall: [], postWriteCanary: [], readiness: {} },
    connectors: [{ id: "fixture", image: `example.test/fixture@sha256:${"a".repeat(64)}`, entrypoint: "/app/connector", network: [], operations: [{ id: "read", description: "Read", effect: "read" }], auth: { label: "Fixture", authorizationOrigin: "https://provider.example", scopes: ["read"], credentialVersion: "1" } }],
  };
  const call = (action: string, input: Record<string, unknown> = {}, owner = "user:one") => service.connectAccount("account-fixture", "fixture", owner, action, input);
  const redirectUri = "http://localhost:3101/api/pack-accounts/account-fixture/fixture/callback";
  const start = async (owner = "user:one", accountId?: string) => await call("start", { redirectUri, accountId }, owner) as { state: string };
  const complete = async (pending: { state: string }, code: string, owner = "user:one") => await call("callback", { state: pending.state, redirectUri, code }, owner) as { accountId: string };
  beforeAll(async () => {
    root = await mkdtemp(join(tmpdir(), "pack-accounts-"));
    vi.stubEnv("XDG_CONFIG_HOME", join(root, "config"));
    await mkdir(join(root, "packs/account-fixture"), { recursive: true });
    await writeFile(join(root, "packs/account-fixture/pack.yaml"), stringify(manifest));
    await pool().query("insert into organization(id,name) values($1,'Pack account test')", [org]);
    service = new PackService(org, join(root, "packs"));
    const review = await service.review("account-fixture");
    await service.install("account-fixture", { reviewHash: review.reviewHash });
    await call("configure", { clientId: "fixture", clientSecret: "private-client-secret" });
  });
  afterAll(async () => {
    await pool().query("delete from organization where id=$1", [org]);
    await pool().end(); vi.unstubAllEnvs(); await rm(root, { recursive: true, force: true });
  });
  it("connects two accounts, consumes state once and enforces owner and organization boundaries", async () => {
    const pending = await start();
    await expect(complete(pending, "one", "user:other")).rejects.toThrow("state");
    await expect(call("callback", { state: pending.state, redirectUri: `${redirectUri}/wrong`, code: "one" })).rejects.toThrow("state");
    first = (await complete(pending, "one")).accountId;
    _resetSecretKeyCacheForTesting();
    service = new PackService(org, join(root, "packs"));
    await expect(complete(pending, "one")).rejects.toThrow("already used");
    second = (await complete(await start(), "two")).accountId;
    expect((await call("list") as { accounts: unknown[] }).accounts).toHaveLength(2);
    expect((await call("list", {}, "user:other") as { accounts: unknown[] }).accounts).toHaveLength(0);
    await expect(service.runConnector("account-fixture", "fixture", "read", {})).rejects.toThrow("Select a pack account");
    await expect(service.runConnector("account-fixture", "fixture", "read", {}, { ownerId: "user:other", accountId: first })).rejects.toThrow("not available");
    expect(await service.runConnector("account-fixture", "fixture", "read", {}, { ownerId: "user:one", accountId: second })).toEqual({ accountId: "two" });
    await expect(new PackService("other-org", join(root, "packs")).connectAccount("account-fixture", "fixture", "user:one", "list", {})).rejects.toThrow("not installed");
    const stored = await pool().query("select value_enc from pack_account where id=$1", [first]);
    expect(stored.rows[0].value_enc).toMatch(/^enc:v1:/);
    expect(stored.rows[0].value_enc).not.toContain("private-one");
    expect(JSON.stringify(await call("list"))).not.toContain("private");
  });
  it("rejects partial consent, expired state and a different account during reconnect", async () => {
    await expect(complete(await start(), "partial")).rejects.toThrow("Required permissions");
    expect(state.revokes).toBe(1);
    const pending = await start();
    const id = pending.state.split(".")[0];
    const { rows } = await pool().query("select value_enc from pack_account where id=$1", [id]);
    const data = JSON.parse(maybeDecryptSecret(rows[0].value_enc));
    await pool().query("update pack_account set value_enc=$2 where id=$1", [id, maybeEncryptSecret(JSON.stringify({ ...data, expiresAt: 1 }))]);
    await expect(complete(pending, "one")).rejects.toThrow("expired");
    await expect(complete(await start("user:one", first), "wrong")).rejects.toThrow("same provider account");
    expect((await complete(await start("user:one", first), "one")).accountId).toBe(first);
  });
  it("serializes rotating refresh across service instances and requires reconnect after an ambiguous failure", async () => {
    async function expire() {
      const { rows } = await pool().query("select value_enc from pack_account where id=$1", [first]);
      await pool().query("update pack_account set value_enc=$2 where id=$1", [first, maybeEncryptSecret(JSON.stringify({ ...JSON.parse(maybeDecryptSecret(rows[0].value_enc)), expiresAt: 1 }))]);
    }
    await expire();
    const binding = { ownerId: "user:one", accountId: first };
    await Promise.all([service, new PackService(org, join(root, "packs"))].map(instance => instance.runConnector("account-fixture", "fixture", "read", {}, binding)));
    expect(state.refreshes).toBe(1);
    await expire(); state.failRefresh = true;
    await expect(service.runConnector("account-fixture", "fixture", "read", {}, binding)).rejects.toThrow("refresh failed");
    await expect(service.runConnector("account-fixture", "fixture", "read", {}, binding)).rejects.toThrow("Reconnect");
    expect(state.refreshes).toBe(2); state.failRefresh = false;
    await complete(await start("user:one", first), "one");
  });
  it("keeps accounts through a compatible upgrade and removes them when client settings change", async () => {
    const upgraded = { ...manifest, metadata: { ...manifest.metadata, version: "1.0.1" } };
    await writeFile(join(root, "packs/account-fixture/pack.yaml"), stringify(upgraded));
    const review = await service.review("account-fixture", {}, "upgrade");
    await service.upgrade("account-fixture", { reviewHash: review.reviewHash });
    expect((await call("list") as { accounts: unknown[] }).accounts).toHaveLength(2);
    state.failRevoke = true;
    await expect(call("disconnect", { accountId: second })).rejects.toThrow("not confirmed");
    expect((await call("list") as { accounts: unknown[] }).accounts).toHaveLength(2);
    state.failRevoke = false;
    await call("disconnect", { accountId: second });
    expect((await call("list") as { accounts: unknown[] }).accounts).toHaveLength(1);
    const pending = await start();
    await call("configure", { clientId: "changed", clientSecret: "changed-secret" });
    expect((await call("list") as { accounts: unknown[] }).accounts).toHaveLength(0);
    await expect(complete(pending, "one")).rejects.toThrow("state");
    await complete(await start(), "one");
    const increased = { ...upgraded, metadata: { ...upgraded.metadata, version: "1.0.2" }, connectors: [{ ...manifest.connectors[0], auth: { ...manifest.connectors[0]!.auth, scopes: ["read", "extra"] } }] };
    await writeFile(join(root, "packs/account-fixture/pack.yaml"), stringify(increased));
    const increasedReview = await service.review("account-fixture", {}, "upgrade");
    await service.upgrade("account-fixture", { reviewHash: increasedReview.reviewHash });
    expect(await call("list")).toMatchObject({ configured: false, accounts: [] });
    await call("configure", { clientId: "increased", clientSecret: "increased-secret" });
    await complete(await start(), "one");
    await start();
    expect((await call("list") as { accounts: unknown[] }).accounts).toHaveLength(1);
    await service.uninstall("account-fixture");
    await expect(call("list")).rejects.toThrow("not installed");
    const { rows } = await pool().query("select count(*)::int as count from pack_account a join pack_install p on p.id=a.pack_install_id where p.org_id=$1", [org]);
    expect(rows[0].count).toBe(0);
  });
});
