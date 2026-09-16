import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  buildPoolConfig,
  pool,
  reconnectPool,
  writeLocalConfig,
} from "../src";

describe("metadata DB pool config", () => {
  let tempHome: string;
  const ORIGINAL_HOME = process.env.HOME;
  const ORIGINAL_XDG = process.env.XDG_CONFIG_HOME;
  const PG_ENV_KEYS = [
    "NEKO_PG_HOST",
    "NEKO_PG_PORT",
    "OPENNEKO_DB_PORT",
    "NEKO_PG_USER",
    "NEKO_PG_PASSWORD",
    "NEKO_PG_DATABASE",
    "NEKO_PG_SSLMODE",
    "OPENNEKO_PG_ENV_OVERRIDE",
  ] as const;
  const ORIGINAL_PG_ENV = Object.fromEntries(
    PG_ENV_KEYS.map((key) => [key, process.env[key]]),
  );

  beforeEach(async () => {
    tempHome = await mkdtemp(join(tmpdir(), "neko-poolcfg-test-"));
    process.env.HOME = tempHome;
    delete process.env.XDG_CONFIG_HOME;
    for (const key of PG_ENV_KEYS) delete process.env[key];
    await reconnectPool();
  });

  afterEach(async () => {
    await reconnectPool();
    if (ORIGINAL_HOME) process.env.HOME = ORIGINAL_HOME;
    if (ORIGINAL_XDG === undefined) delete process.env.XDG_CONFIG_HOME;
    else process.env.XDG_CONFIG_HOME = ORIGINAL_XDG;
    for (const key of PG_ENV_KEYS) {
      const original = ORIGINAL_PG_ENV[key];
      if (original === undefined) delete process.env[key];
      else process.env[key] = original;
    }
    await rm(tempHome, { recursive: true, force: true });
  });

  it("uses environment config when no local config exists", () => {
    process.env.NEKO_PG_HOST = "metadata.internal";
    process.env.NEKO_PG_PORT = "55432";
    process.env.NEKO_PG_USER = "runtime-user";
    process.env.NEKO_PG_PASSWORD = "runtime-password";
    process.env.NEKO_PG_DATABASE = "runtime-db";
    process.env.NEKO_PG_SSLMODE = "require";

    expect(buildPoolConfig()).toMatchObject({
      host: "metadata.internal",
      port: 55432,
      user: "runtime-user",
      password: "runtime-password",
      database: "runtime-db",
      ssl: { rejectUnauthorized: false },
    });
  });

  it("prefers local config over environment config", () => {
    process.env.NEKO_PG_HOST = "metadata.internal";
    process.env.NEKO_PG_PORT = "55432";
    writeLocalConfig({ pg: { host: "localhost", port: 5433 } });

    expect(buildPoolConfig()).toMatchObject({
      host: "localhost",
      port: 5433,
    });
  });

  it("can prefer explicit host-development environment values", () => {
    process.env.NEKO_PG_HOST = "127.0.0.1";
    process.env.NEKO_PG_PORT = "56432";
    process.env.OPENNEKO_PG_ENV_OVERRIDE = "1";
    writeLocalConfig({ pg: { host: "neko-db", port: 5432 } });

    expect(buildPoolConfig()).toMatchObject({
      host: "127.0.0.1",
      port: 56432,
    });
  });

  it("reuses the pool while config is unchanged", () => {
    const first = pool();
    expect(pool()).toBe(first);
  });

  it("survives an idle connection error instead of killing the process", () => {
    const live = pool();
    expect(live.listenerCount("error")).toBeGreaterThan(0);
    // Without a listener node reports this as unhandled and exits.
    expect(() => live.emit("error", new Error("Connection terminated unexpectedly"))).not.toThrow();
  });

  it("rebuilds the pool when local Postgres config changes", () => {
    const first = pool();
    writeLocalConfig({ pg: { password: "changed-password" } });
    const second = pool();

    expect(second).not.toBe(first);
    expect(pool()).toBe(second);
  });
});
