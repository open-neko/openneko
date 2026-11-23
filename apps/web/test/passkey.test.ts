import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createApprovalPasskeyAssertion,
  deletePasskey,
  fromBase64Url,
  isPasskeySupported,
  listPasskeys,
  registerPasskey,
  sendApproval,
  toBase64Url,
} from "../src/lib/passkey";

describe("passkey client helpers", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("round-trips base64url buffers without padding", () => {
    const raw = new Uint8Array([0, 1, 2, 250, 251, 252, 253, 254, 255]);
    const encoded = toBase64Url(raw);
    expect(encoded).not.toContain("+");
    expect(encoded).not.toContain("/");
    expect(encoded).not.toContain("=");
    expect(Array.from(fromBase64Url(encoded))).toEqual(Array.from(raw));
  });

  it("detects browser WebAuthn support", () => {
    vi.stubGlobal("window", {});
    expect(isPasskeySupported()).toBe(false);

    vi.stubGlobal("window", { PublicKeyCredential: function PublicKeyCredential() {} });
    vi.stubGlobal("navigator", {
      credentials: {
        create: vi.fn(),
        get: vi.fn(),
      },
    });
    expect(isPasskeySupported()).toBe(true);
  });

  it("enrolls a passkey on first approval and then signs the approval challenge", async () => {
    const rawId = new Uint8Array([10, 20, 30, 40]).buffer;
    const createMock = vi.fn().mockResolvedValue({
      rawId,
      response: {
        clientDataJSON: new TextEncoder().encode('{"type":"webauthn.create"}').buffer,
        attestationObject: new Uint8Array([0xa1, 0x68]).buffer,
      },
    });
    const getMock = vi.fn().mockResolvedValue({
      rawId,
      response: {
        clientDataJSON: new TextEncoder().encode('{"type":"webauthn.get"}').buffer,
        authenticatorData: new Uint8Array(37).fill(1).buffer,
        signature: new Uint8Array([0x30, 0x06, 0x02, 0x01, 0x01, 0x02, 0x01, 0x01]).buffer,
      },
    });
    vi.stubGlobal("window", { PublicKeyCredential: function PublicKeyCredential() {} });
    vi.stubGlobal("navigator", {
      credentials: {
        create: createMock,
        get: getMock,
      },
    });

    const credIdB64 = toBase64Url(rawId);
    let registered = false;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url === "/api/v1/my/passkeys/challenge") {
        const body = JSON.parse(String(init?.body ?? "{}")) as {
          purpose: string;
          actionRequestId?: string;
        };
        return new Response(
          JSON.stringify({
            purpose: body.purpose,
            challenge: toBase64Url(new Uint8Array(32).fill(5)),
            rp: { id: "localhost", name: "OpenNeko" },
            user: {
              id: toBase64Url(new TextEncoder().encode("user-1")),
              name: "user@example.com",
              displayName: "User",
            },
            pubKeyCredParams: [{ type: "public-key", alg: -7 }],
            excludeCredentials: [],
            allowCredentials: registered ? [{ id: credIdB64, type: "public-key" }] : [],
            userVerification: "required",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (url === "/api/v1/my/passkeys" && init?.method === "POST") {
        registered = true;
        return new Response(
          JSON.stringify({
            passkey: {
              id: "pk-1",
              credentialId: credIdB64,
              name: "This device",
              createdAt: "2026-11-14T00:00:00.000Z",
              lastUsedAt: null,
            },
          }),
          { status: 201, headers: { "Content-Type": "application/json" } },
        );
      }
      if (url === "/api/v1/my/passkeys") {
        return new Response(
          JSON.stringify({
            passkeys: [
              {
                id: "pk-1",
                credentialId: credIdB64,
                name: "This device",
                createdAt: "2026-11-14T00:00:00.000Z",
                lastUsedAt: null,
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (url === "/api/v1/my/passkeys/pk-1" && init?.method === "DELETE") {
        return new Response(JSON.stringify({ ok: true }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      throw new Error(`Unexpected fetch: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const assertion = await createApprovalPasskeyAssertion("30000000-0000-4000-8000-000000000001");
    expect(createMock).toHaveBeenCalledTimes(1);
    expect(getMock).toHaveBeenCalledTimes(1);
    expect(assertion.credentialId).toBe(credIdB64);
    expect(createMock.mock.calls[0]![0].publicKey.authenticatorSelection.userVerification).toBe("required");
    expect(getMock.mock.calls[0]![0].publicKey.userVerification).toBe("required");

    // Second approval uses the existing passkey without calling create() again
    const second = await createApprovalPasskeyAssertion("30000000-0000-4000-8000-000000000002");
    expect(createMock).toHaveBeenCalledTimes(1);
    expect(getMock).toHaveBeenCalledTimes(2);
    expect(second.credentialId).toBe(credIdB64);

    const listed = await listPasskeys();
    expect(listed).toHaveLength(1);
    expect(listed[0]?.credentialId).toBe(credIdB64);

    const explicit = await registerPasskey("Phone");
    expect(explicit.credentialId).toBe(credIdB64);

    await deletePasskey("pk-1");
  });

  function stubPasskeyBrowser(purposes: string[]) {
    const rawId = new Uint8Array([7, 7, 7]).buffer;
    const getMock = vi.fn().mockResolvedValue({
      rawId,
      response: {
        clientDataJSON: new Uint8Array([1]).buffer,
        authenticatorData: new Uint8Array([2]).buffer,
        signature: new Uint8Array([3]).buffer,
      },
    });
    vi.stubGlobal("window", { PublicKeyCredential: function PublicKeyCredential() {} });
    vi.stubGlobal("navigator", { credentials: { create: vi.fn(), get: getMock } });
    const challenge = (purpose: string) =>
      new Response(
        JSON.stringify({
          purpose,
          challenge: toBase64Url(new Uint8Array(32).fill(9)),
          rp: { id: "localhost", name: "OpenNeko" },
          user: { id: "dQ", name: "u", displayName: "u" },
          pubKeyCredParams: [],
          excludeCredentials: [],
          allowCredentials: [{ id: toBase64Url(rawId), type: "public-key" }],
          userVerification: "required",
        }),
        { status: 200 },
      );
    return {
      getMock,
      challengeFetch: (init?: RequestInit) => {
        const body = JSON.parse(String(init?.body ?? "{}")) as { purpose: string };
        purposes.push(body.purpose);
        return challenge(body.purpose);
      },
    };
  }

  it("sends an approval again with a passkey when the server asks for one", async () => {
    const purposes: string[] = [];
    const { getMock, challengeFetch } = stubPasskeyBrowser(purposes);
    vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => challengeFetch(init)));
    const sent: unknown[] = [];
    const send = vi.fn(async (passkey?: unknown) => {
      sent.push(passkey);
      return passkey
        ? new Response(JSON.stringify({ ok: true }), { status: 200 })
        : new Response(JSON.stringify({ error: "This approval needs your passkey.", code: "passkey_required" }), { status: 403 });
    });
    const res = await sendApproval("30000000-0000-4000-8000-000000000003", send);
    expect(res.status).toBe(200);
    expect(sent).toHaveLength(2);
    expect(sent[0]).toBeUndefined();
    expect(sent[1]).toMatchObject({ credentialId: "BwcH" });
    expect(purposes).toEqual(["approve"]);
    expect(getMock).toHaveBeenCalledTimes(1);

    const plain = vi.fn(async () => new Response(JSON.stringify({ error: "nope" }), { status: 403 }));
    expect((await sendApproval("30000000-0000-4000-8000-000000000004", plain)).status).toBe(403);
    expect(plain).toHaveBeenCalledTimes(1);
  });

  it("signs a remove challenge before it removes the last passkey", async () => {
    const purposes: string[] = [];
    const { challengeFetch } = stubPasskeyBrowser(purposes);
    const deletes: Array<string | undefined> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        if (String(input) === "/api/v1/my/passkeys/challenge") return challengeFetch(init);
        deletes.push(init?.body ? String(init.body) : undefined);
        return new Response(JSON.stringify({ ok: true }), { status: 200 });
      }),
    );
    await deletePasskey("pk-2", true);
    expect(purposes).toEqual(["remove"]);
    expect(deletes).toHaveLength(1);
    expect(JSON.parse(deletes[0]!)).toMatchObject({ passkey: { credentialId: "BwcH" } });
  });
});
