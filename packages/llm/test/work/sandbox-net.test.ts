import { describe, expect, it } from "vitest";
import {
  isHostLocalName,
  isLoopbackHost,
  SANDBOX_HOST_ALIAS,
  sandboxReachableUrl,
} from "../../src/work/sandbox-net";

describe("sandbox-net", () => {
  it("classifies loopback hosts", () => {
    for (const h of ["localhost", "LOCALHOST", "127.0.0.1", "127.1.2.3", "0.0.0.0", "::1", "[::1]"]) {
      expect(isLoopbackHost(h), h).toBe(true);
    }
    for (const h of ["host.openshell.internal", "db.example.com", "10.0.0.5", "127a"]) {
      expect(isLoopbackHost(h), h).toBe(false);
    }
  });

  it("rewrites loopback URLs to the gateway host alias, keeping port and path", () => {
    expect(sandboxReachableUrl("http://localhost:8080/api/v1/mcp")).toBe(
      `http://${SANDBOX_HOST_ALIAS}:8080/api/v1/mcp`,
    );
    expect(sandboxReachableUrl("https://127.0.0.1/x")).toBe(
      `https://${SANDBOX_HOST_ALIAS}/x`,
    );
  });

  it("passes non-loopback and unparseable values through", () => {
    expect(sandboxReachableUrl("https://gj.prod.example:443/api")).toBe(
      "https://gj.prod.example:443/api",
    );
    expect(sandboxReachableUrl("not a url")).toBe("not a url");
  });

  it("classifies dot-less compose service names as host-local", () => {
    for (const h of ["graphjin", "neko-db", "localhost", "127.0.0.1"]) {
      expect(isHostLocalName(h), h).toBe(true);
    }
    for (const h of ["db.example.com", "host.openshell.internal", "10.0.0.5"]) {
      expect(isHostLocalName(h), h).toBe(false);
    }
  });

  it("rewrites compose-internal URLs to the gateway host alias", () => {
    expect(sandboxReachableUrl("http://graphjin:8080/api/v1/mcp")).toBe(
      `http://${SANDBOX_HOST_ALIAS}:8080/api/v1/mcp`,
    );
  });
});
