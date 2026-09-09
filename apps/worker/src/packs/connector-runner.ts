import { execFile } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import { packConnectorSchema, type PackConnector } from "@neko/packs";

const exec = promisify(execFile);
const MAX_BYTES = 1024 * 1024;

/** One invocation per sandbox. Request data never appears in command arguments. */
export async function runPackConnector(
  declaration: PackConnector,
  request?: { operation: string; input: Record<string, unknown>; credential?: Record<string, unknown>; action?: { requestId: string; executionId: string } }
    | { connection: "authorize" | "exchange" | "refresh" | "revoke"; input: Record<string, unknown> },
  approvedWrite = false,
): Promise<unknown> {
  const connector = packConnectorSchema.parse(declaration);
  if (request && "operation" in request) {
    const operation = connector.operations.find(value => value.id === request.operation);
    if (!operation) throw new Error("Pack connector operation is not declared");
    if (operation.effect !== "read" && !approvedWrite) throw new Error("Pack connector writes require action dispatch support");
  }
  if (request && "connection" in request && !connector.auth) throw new Error("Pack connector does not support accounts");
  const payload = JSON.stringify(request ?? {});
  if (Buffer.byteLength(payload) > MAX_BYTES) throw new Error("Pack connector request exceeds 1 MiB");
  const directory = await mkdtemp(join(tmpdir(), "openneko-pack-"));
  const name = `pack-${randomUUID()}`;
  const gateway = process.env.OPENSHELL_GATEWAY;
  const endpoint = process.env.OPENSHELL_GATEWAY_ENDPOINT;
  const prefix = gateway ? ["--gateway", gateway] : endpoint ? ["--gateway-endpoint", endpoint] : [];
  const run = async (args: string[], timeout: number) => {
    try {
      const child = exec(process.env.OPENSHELL_CLI || "openshell", [...prefix, ...args], {
        timeout, maxBuffer: MAX_BYTES, killSignal: "SIGKILL",
      });
      child.child?.stdin?.end();
      return (await child).stdout;
    } catch {
      // CLI output can contain provider data. Return only the failed phase.
      throw new Error(`Pack connector ${args[1]} failed or exceeded its limit`);
    }
  };
  let failed = false;
  try {
    const policy = {
      version: 1,
      filesystem_policy: {
        include_workdir: false,
        read_only: ["/app", "/usr", "/lib", "/etc", "/proc", "/dev/urandom"],
        read_write: ["/sandbox", "/tmp", "/dev/null"],
      },
      landlock: { compatibility: "best_effort" },
      process: { run_as_user: "sandbox", run_as_group: "sandbox" },
      network_policies: Object.fromEntries(connector.network.map((rule, i) => [`endpoint_${i}`, {
        name: `endpoint_${i}`, binaries: [{ path: rule.binary }],
        endpoints: [{ host: rule.host, port: rule.port, protocol: "rest", enforcement: "enforce", rules: [{ allow: { method: "*", path: "/**" } }] }],
      }])),
    };
    const policyFile = join(directory, "policy.json");
    await writeFile(policyFile, JSON.stringify(policy), { mode: 0o600 });
    await run(["sandbox", "create", "--name", name, "--from", connector.image,
      "--policy", policyFile, "--cpu", "1", "--memory", "512Mi", "--no-tty", "--no-auto-providers",
      "--", "/usr/bin/test", "-x", connector.entrypoint], 120_000);
    if (!request) return;
    const file = join(directory, "request.json");
    await writeFile(file, payload, { mode: 0o600 });
    await run(["sandbox", "upload", name, file, "/sandbox/request.json"], 30_000);
    const output = await run(["sandbox", "exec", "--name", name, "--no-tty", "--timeout", "60",
      "--", connector.entrypoint, "/sandbox/request.json"], 65_000);
    try { return JSON.parse(output); }
    catch { throw new Error("Pack connector must return one JSON value"); }
  } catch (error) {
    failed = true;
    throw error;
  } finally {
    try { await run(["sandbox", "delete", name], 30_000); }
    catch (error) { if (!failed) throw error; }
    finally { await rm(directory, { recursive: true, force: true }); }
  }
}
