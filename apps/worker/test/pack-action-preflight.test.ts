import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { assertPackActionReady, type PackActionReadiness } from "../src/packs/action-preflight.js";
import { lockBody } from "../src/packs/declarative-action-runtime.js";

const here = dirname(fileURLToPath(import.meta.url));
const parityFixturePath = resolve(
  here,
  "../../openneko/internal/packs/testdata/pack_action_preflight_parity.json",
);

describe("pack action preflight", () => {
  it("accepts an enabled ready action", () => {
    expect(() =>
      assertPackActionReady("magento.add_internal_order_comment", {
        enabled: true,
        readiness: "ready",
        reason: null,
      }),
    ).not.toThrow();
  });

  it("rejects an action while its operator is blocked", () => {
    expect(() =>
      assertPackActionReady("magento.add_internal_order_comment", {
        enabled: true,
        readiness: "blocked",
        reason: "GraphJin REST mutations require a newer release",
      }),
    ).toThrow(
      "pack action magento.add_internal_order_comment is blocked: GraphJin REST mutations require a newer release",
    );
  });

  it("rejects a disabled action even when marked ready", () => {
    expect(() =>
      assertPackActionReady("magento.add_internal_order_comment", {
        enabled: false,
        readiness: "ready",
        reason: null,
      }),
    ).toThrow("pack action magento.add_internal_order_comment is blocked");
  });

  it("matches the Go pack action preflight and lockBody parity fixture", () => {
    const fixture = JSON.parse(readFileSync(parityFixturePath, "utf8")) as {
      readinessCases: Array<{
        name: string;
        kind: string;
        definition: PackActionReadiness;
        expectedError: string | null;
      }>;
      lockBodyCases: Array<{
        name: string;
        kind: string;
        hasBody: boolean;
        body: unknown;
        locked: Record<string, { input: string }>;
        config: Record<string, unknown>;
        expectedHasBody: boolean;
        expectedBody: unknown;
        expectedError: string | null;
      }>;
    };

    for (const tc of fixture.readinessCases) {
      if (tc.expectedError === null) {
        expect(() => assertPackActionReady(tc.kind, tc.definition), tc.name).not.toThrow();
      } else {
        expect(() => assertPackActionReady(tc.kind, tc.definition), tc.name).toThrow(tc.expectedError);
      }
    }

    for (const tc of fixture.lockBodyCases) {
      const bodyArg = tc.hasBody ? tc.body : undefined;
      if (tc.expectedError !== null) {
        expect(() => lockBody(tc.kind, bodyArg, tc.locked, tc.config), tc.name).toThrow(tc.expectedError);
      } else {
        const result = lockBody(tc.kind, bodyArg, tc.locked, tc.config);
        expect(result !== undefined, tc.name).toBe(tc.expectedHasBody);
        if (tc.expectedHasBody) {
          expect(result, tc.name).toEqual(tc.expectedBody);
        }
      }
    }
  });
});
