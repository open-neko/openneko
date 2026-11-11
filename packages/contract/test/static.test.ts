import { describe, expect, it } from "vitest";
import { cases } from "../cases";
import { REVOKED_TOKEN, targetFromEnv } from "../src/client";
import { loadContract, operations } from "../src/spec";
import { validate } from "../src/validate";

const contract = loadContract();

describe("contract cases", () => {
  it("cover every operation in the contract", () => {
    const covered = new Set(cases.map((c) => c.op));
    expect(operations(contract).map((op) => op.key).filter((key) => !covered.has(key))).toEqual([]);
  });

  it("name only operations in the contract", () => {
    const known = new Set(operations(contract).map((op) => op.key));
    expect(cases.map((c) => c.op).filter((key) => !known.has(key))).toEqual([]);
  });

  it("expect only statuses that the contract lists", () => {
    const byKey = new Map(operations(contract).map((op) => [op.key, op]));
    const undocumented = cases
      .filter((c) => !byKey.get(c.op)?.responses[String(c.status)] && !byKey.get(c.op)?.responses.default)
      .map((c) => `${c.op} ${c.status}`);
    expect(undocumented).toEqual([]);
  });

  it("give every case a unique name", () => {
    const names = cases.map((c) => `${c.op}: ${c.name}`);
    expect(names.length).toBe(new Set(names).size);
  });
});

describe("contract file", () => {
  it("documents 401 on every signed-in and admin operation, and 403 on every admin operation", () => {
    const gaps = operations(contract).flatMap((op) => [
      ...((op.access === "signed-in" || op.access === "admin") && !op.responses["401"] ? [`${op.key} 401`] : []),
      ...(op.access === "admin" && !op.responses["403"] ? [`${op.key} 403`] : []),
    ]);
    expect(gaps).toEqual([]);
  });

  it("closes the top level of every JSON success body", () => {
    const open = operations(contract).flatMap((op) =>
      Object.entries(op.responses)
        .filter(([status]) => status.startsWith("2"))
        .flatMap(([status, response]) => {
          const schema = response.content?.["application/json"]?.schema;
          if (!schema || schema.$ref || schema.type !== "object") return [];
          return schema.additionalProperties === false ? [] : [`${op.key} ${status}`];
        }),
    );
    expect(open).toEqual([]);
  });
});

describe("validate", () => {
  const components = { Error: { type: "object", required: ["error"], properties: { error: { type: "string" } } } };

  it("checks types, required properties and closed objects", () => {
    const schema = {
      type: "object",
      required: ["id", "tags"],
      additionalProperties: false,
      properties: { id: { type: "string" }, count: { type: ["integer", "null"] }, tags: { type: "array", items: { type: "string" } } },
    };
    expect(validate({ id: "a", count: null, tags: ["x"] }, schema)).toEqual([]);
    expect(validate({ id: 1, tags: [2], extra: true }, schema)).toEqual([
      "$.id: expected string, got integer",
      "$.tags[0]: expected string, got integer",
      "$: property extra is not in the contract",
    ]);
    expect(validate({ id: "a" }, schema)).toEqual(["$: missing required property tags"]);
    expect(validate({ id: "a", tags: [], count: 1.5 }, schema)).toEqual(["$.count: expected integer or null, got number"]);
  });

  it("follows a component reference and checks enums", () => {
    expect(validate({ error: "no" }, { $ref: "#/components/schemas/Error" }, components)).toEqual([]);
    expect(validate({}, { $ref: "#/components/schemas/Error" }, components)).toEqual(["$: missing required property error"]);
    expect(validate("b", { enum: ["a"] })).toEqual(['$: "b" is not one of ["a"]']);
  });
});

describe("target", () => {
  it("is off without a server address and needs both tokens with one", () => {
    expect(targetFromEnv({})).toBeNull();
    expect(() => targetFromEnv({ OPENNEKO_CONTRACT_URL: "http://x" })).toThrow(/ADMIN_TOKEN/);
    expect(targetFromEnv({ OPENNEKO_CONTRACT_URL: "http://x/", OPENNEKO_CONTRACT_ADMIN_TOKEN: "a", OPENNEKO_CONTRACT_MEMBER_TOKEN: "m" }))
      .toEqual({ baseUrl: "http://x", tokens: { admin: "a", member: "m" } });
    expect(REVOKED_TOKEN).toMatch(/^onk_dev_[0-9a-f]{12}_/);
  });
});
