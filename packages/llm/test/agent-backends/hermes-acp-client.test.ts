import { describe, expect, it } from "vitest";
import { PassThrough } from "node:stream";
import type { ChildProcess } from "node:child_process";
import { createAcpClient } from "../../src/agent-backends/hermes-acp-client";

function makeFakeChild() {
  const stdout = new PassThrough();
  const stdin = new PassThrough();
  const written: string[] = [];
  stdin.on("data", (d: Buffer) => written.push(d.toString("utf8")));
  const child = {
    stdout,
    stdin,
    on: () => child,
  } as unknown as ChildProcess;
  return { child, stdout, written };
}

const flush = () => new Promise((r) => setTimeout(r, 10));

describe("createAcpClient inbound requests", () => {
  it("approves session/request_permission with the allow_once option", async () => {
    const { child, stdout, written } = makeFakeChild();
    createAcpClient(child);
    stdout.write(
      JSON.stringify({
        jsonrpc: "2.0",
        id: 7,
        method: "session/request_permission",
        params: {
          sessionId: "s1",
          options: [
            { optionId: "reject", kind: "reject_once" },
            { optionId: "yes-once", kind: "allow_once" },
            { optionId: "yes-always", kind: "allow_always" },
          ],
        },
      }) + "\n",
    );
    await flush();
    const res = JSON.parse(written.join("").trim());
    expect(res).toEqual({
      jsonrpc: "2.0",
      id: 7,
      result: { outcome: { outcome: "selected", optionId: "yes-once" } },
    });
  });

  it("answers unknown inbound requests with method-not-found instead of silence", async () => {
    const { child, stdout, written } = makeFakeChild();
    createAcpClient(child);
    stdout.write(
      JSON.stringify({ jsonrpc: "2.0", id: 9, method: "fs/read_text_file", params: {} }) + "\n",
    );
    await flush();
    const res = JSON.parse(written.join("").trim());
    expect(res.id).toBe(9);
    expect(res.error.code).toBe(-32601);
  });

  it("still resolves our own outbound requests from response frames", async () => {
    const { child, stdout, written } = makeFakeChild();
    const client = createAcpClient(child);
    const p = client.request("session/new", {});
    await flush();
    const sent = JSON.parse(written.join("").trim());
    stdout.write(
      JSON.stringify({ jsonrpc: "2.0", id: sent.id, result: { sessionId: "s2" } }) + "\n",
    );
    await expect(p).resolves.toEqual({ sessionId: "s2" });
  });
});
