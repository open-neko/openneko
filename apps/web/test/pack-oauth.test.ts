import { afterEach, describe, expect, it, vi } from "vitest";

const jar = vi.hoisted(() => new Map<string, string>());
vi.mock("server-only", () => ({}));
vi.mock("next/headers", () => ({
  cookies: async () => ({
    get: (name: string) => (jar.has(name) ? { value: jar.get(name) } : undefined),
    delete: (name: string) => jar.delete(name),
  }),
}));

import { packOAuthCallbackUri, readAndClearPackOAuthState } from "../src/lib/pack-oauth";

const goPackState = "eyJjb2RlVmVyaWZpZXIiOiJjdiIsImNvbm5lY3Rpb25LZXkiOiJhY2NvdW50IiwiZXhwaXJlc0F0Ijo0MTAyNDQ0ODAwMDAwLCJvcmdJZCI6Im9yZyIsInBhY2tJZCI6ImRyaXZlIiwicGVyc29uYWwiOnRydWUsInJldHVyblBhdGgiOiIvaW50ZWdyYXRpb25zIiwic3RhdGUiOiJzdCIsInVzZXJJZCI6InUxIn0.yI0caLBmra1sp8q3_hh_YfWHoSNoq6t7V4o_xCev_d8";

describe("readAndClearPackOAuthState", () => {
  afterEach(() => vi.unstubAllEnvs());

  it("reads the state cookie that openneko web writes", async () => {
    vi.stubEnv("OPENNEKO_SESSION_SECRET", "0123456789abcdef0123456789abcdef");
    jar.set("openneko_pack_oauth_state", goPackState);
    expect(await readAndClearPackOAuthState()).toEqual({
      packId: "drive", connectionKey: "account", state: "st", codeVerifier: "cv", returnPath: "/integrations",
      orgId: "org", userId: "u1", personal: true, expiresAt: 4102444800000,
    });
    expect(jar.has("openneko_pack_oauth_state")).toBe(false);
  });
});

describe("packOAuthCallbackUri", () => {
  afterEach(() => vi.unstubAllEnvs());

  it("uses the browser-facing host when the server request URL is internal", () => {
    const request = new Request("http://0.0.0.0:8080/api/pack-accounts/google-workspace/workspace", {
      headers: { host: "localhost:3102" },
    });

    expect(packOAuthCallbackUri(request, "google-workspace", "workspace")).toBe(
      "http://localhost:3102/api/pack-accounts/google-workspace/workspace/callback",
    );
  });

  it("uses forwarded host and protocol behind a reverse proxy", () => {
    const request = new Request("http://web:8080/api/pack-accounts/google-workspace/workspace", {
      headers: {
        host: "web:8080",
        "x-forwarded-host": "neko.example.com",
        "x-forwarded-proto": "https",
      },
    });

    expect(packOAuthCallbackUri(request, "google-workspace", "workspace")).toBe(
      "https://neko.example.com/api/pack-accounts/google-workspace/workspace/callback",
    );
  });

  it("uses the configured public URL when present", () => {
    vi.stubEnv("OPENNEKO_PUBLIC_URL", "https://public.example.com/");
    const request = new Request("http://web:8080/api/pack-accounts/google-workspace/workspace");

    expect(packOAuthCallbackUri(request, "google-workspace", "workspace")).toBe(
      "https://public.example.com/api/pack-accounts/google-workspace/workspace/callback",
    );
  });
});
