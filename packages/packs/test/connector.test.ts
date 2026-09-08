import { expect, it } from "vitest";
import { packConnectorSchema } from "../src/connector";

const connector = { id: "notes", image: `example.test/notes@sha256:${"a".repeat(64)}`, entrypoint: "/app/connector", operations: [{ id: "read", description: "Read notes", effect: "read" }] };
it("accepts a fixed image and rejects unsafe declarations", () => {
  expect(packConnectorSchema.parse(connector).network).toEqual([]);
  for (const change of [
    { image: "example.test/notes:latest" },
    { entrypoint: "/app/../tmp/connector" },
    { operations: [...connector.operations, ...connector.operations] },
    { network: [{ host: "*", port: 443, binary: "/usr/bin/curl" }] },
  ]) expect(() => packConnectorSchema.parse({ ...connector, ...change })).toThrow();
});
