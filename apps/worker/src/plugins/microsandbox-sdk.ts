import { existsSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";

/**
 * Microsandbox SDK shape we depend on. Mirrored intentionally (not a
 * direct import of the npm package's types) so the worker can be
 * tested with an in-process fake — every method that the SDK omits at
 * older versions is guarded with a `typeof === "function"` check.
 *
 * Lifted from aithy's src/sandbox/microsandbox-sdk.ts (same author,
 * same defensive patterns), trimmed to the surface OpenNeko's plugin
 * runtime uses (no per-file fs operations — the runtime only execs).
 */

export interface MicrosandboxExecOutput {
  code?: number;
  stdout(): string;
  stderr(): string;
}

export interface MicrosandboxInstance {
  exec(cmd: string, args: string[]): Promise<MicrosandboxExecOutput>;
  stopAndWait?(): Promise<void>;
  stop?(): Promise<void>;
  removePersisted?(): Promise<void>;
}

export interface NetworkPolicyValue {
  __policy: "none" | "publicOnly" | "allowAll";
}

export interface MicrosandboxBuilder {
  image(value: string): MicrosandboxBuilder;
  cpus(value: number): MicrosandboxBuilder;
  memory(value: number): MicrosandboxBuilder;
  replace(): MicrosandboxBuilder;
  network(
    configure: (network: { policy(policy: unknown): unknown }) => unknown,
  ): MicrosandboxBuilder;
  volume(
    path: string,
    configure: (volume: { bind(path: string): unknown }) => unknown,
  ): MicrosandboxBuilder;
  libkrunfwPath?(path: string): MicrosandboxBuilder;
  create(): Promise<MicrosandboxInstance>;
}

export interface MicrosandboxFactory {
  builder(name: string): MicrosandboxBuilder;
  get?(name: string): Promise<{ startDetached(): Promise<MicrosandboxInstance> }>;
  remove?(name: string): Promise<void>;
}

export interface NetworkPolicyApi {
  none(): NetworkPolicyValue;
  publicOnly(): NetworkPolicyValue;
  allowAll(): NetworkPolicyValue;
}

export function applyBundledRuntime(
  builder: MicrosandboxBuilder,
): MicrosandboxBuilder {
  if (typeof builder.libkrunfwPath !== "function") return builder;
  const libkrunfwPath = resolveBundledLibkrunfwPath();
  return libkrunfwPath ? builder.libkrunfwPath(libkrunfwPath) : builder;
}

export async function stopSandbox(
  sandbox: MicrosandboxInstance,
): Promise<void> {
  if (typeof sandbox.stopAndWait === "function") await sandbox.stopAndWait();
  else if (typeof sandbox.stop === "function") await sandbox.stop();
  if (typeof sandbox.removePersisted === "function") {
    await sandbox.removePersisted();
  }
}

export function formatMicrosandboxStartError(error: unknown): string {
  const message = formatError(error);
  if (message.includes("libkrunfw not found")) {
    return (
      `${message}. The bundled microsandbox platform package was installed, ` +
      `but the runtime did not find libkrunfw. Check ` +
      `node_modules/@superradcompany/microsandbox-<triple>/lib/.`
    );
  }
  if (message.includes("Operation not permitted")) {
    return (
      `${message}. Microsandbox could not start a microVM with the current ` +
      `host permissions. On macOS this usually means virtualization permission ` +
      `is missing; on Linux check KVM access (/dev/kvm).`
    );
  }
  return message;
}

export function formatError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function resolveBundledLibkrunfwPath(): string | undefined {
  const triple = platformTriple();
  if (!triple) return undefined;
  try {
    const require = createRequire(import.meta.url);
    const packagePath = require.resolve(
      `@superradcompany/microsandbox-${triple}/package.json`,
    );
    const root = path.dirname(packagePath);
    const name =
      process.platform === "darwin" ? "libkrunfw.5.dylib" : "libkrunfw.so";
    const candidate = path.join(root, "lib", name);
    return existsSync(candidate) ? candidate : undefined;
  } catch {
    return undefined;
  }
}

export function platformTriple(): string | undefined {
  if (process.platform === "darwin" && process.arch === "arm64") {
    return "darwin-arm64";
  }
  if (process.platform === "linux" && process.arch === "x64") {
    return "linux-x64-gnu";
  }
  if (process.platform === "linux" && process.arch === "arm64") {
    return "linux-arm64-gnu";
  }
  return undefined;
}

export function isSupportedHost(): boolean {
  return platformTriple() !== undefined;
}
