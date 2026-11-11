// Protocol: apps/ax-harness/serve/serve.go. A key leaves this process only in
// the frame's key fields or the child's environment, never in the request.

import { spawn } from "node:child_process";
import { createConnection } from "node:net";
import { createInterface } from "node:readline";
import { serveSocket } from "./agent-backends/harness-turn";
import { AX_HARNESS_BINARY } from "./agent-runtime-contract";

export type GatewayCall = {
  frame: Record<string, unknown>;
  mode: "generate" | "embed";
  env: Record<string, string>;
  input: unknown;
  label: string;
  timeoutMs: number;
};

function serveCall<T>(socket: string, call: GatewayCall): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const conn = createConnection(socket);
    let result: T | undefined;
    let settled = false;
    const finish = (error?: Error) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      conn.destroy();
      if (error) reject(error);
      else if (result) resolve(result);
      else reject(new Error(`openneko serve returned no ${call.label} result`));
    };
    const timer = setTimeout(() => finish(new Error(`${call.label} call timed out`)), call.timeoutMs);
    conn.on("error", (error) => finish(new Error(`openneko serve unavailable: ${error.message}`)));
    conn.on("close", () => finish());
    conn.on("connect", () => {
      conn.write(`${JSON.stringify(call.frame)}\n`);
    });
    const lines = createInterface({ input: conn });
    lines.on("line", (line) => {
      if (!line.trim()) return;
      let frame: { type?: string; result?: T; code?: number; error?: string };
      try {
        frame = JSON.parse(line);
      } catch {
        finish(new Error("openneko serve wrote a line that is not JSON"));
        return;
      }
      if (frame.type === "result" && frame.result) result = frame.result;
      else if (frame.type === "exit") {
        finish(frame.code === 0 ? undefined : new Error(frame.error || `${call.label} call failed (code ${frame.code})`));
      }
    });
  });
}

function binaryCall<T>(binary: string, call: GatewayCall): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const child = spawn(binary, [call.mode], {
      stdio: ["pipe", "pipe", "pipe"],
      env: { PATH: process.env.PATH ?? "", ...call.env } as unknown as NodeJS.ProcessEnv,
      timeout: call.timeoutMs,
    });
    const stdout: Buffer[] = [];
    const stderr: Buffer[] = [];
    child.stdout.on("data", (chunk: Buffer) => stdout.push(chunk));
    child.stderr.on("data", (chunk: Buffer) => stderr.push(chunk));
    child.on("error", (error) => reject(new Error(`ax-harness unavailable: ${error.message}`)));
    child.on("close", (code) => {
      if (code !== 0) {
        reject(new Error(Buffer.concat(stderr).toString("utf8").trim().slice(-2000) || `${call.label} call failed (code ${code})`));
        return;
      }
      try {
        resolve(JSON.parse(Buffer.concat(stdout).toString("utf8")) as T);
      } catch {
        reject(new Error("ax-harness returned a result that is not JSON"));
      }
    });
    child.stdin.on("error", () => {});
    child.stdin.end(JSON.stringify(call.input));
  });
}

/** One call to the Go gateway: over serve's socket when it is set, else a one-shot ax-harness. */
export function gatewayCall<T>(call: GatewayCall): Promise<T> {
  const socket = serveSocket();
  return socket ? serveCall<T>(socket, call) : binaryCall<T>(process.env.OPENNEKO_AX_HARNESS_BINARY || AX_HARNESS_BINARY, call);
}
