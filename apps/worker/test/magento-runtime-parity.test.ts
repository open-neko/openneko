import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

import {
  isTerminalMagentoBulkStatus,
  magentoGraphjinMutationField,
  reconciliationConfirmed,
} from "../src/packs/magento-v2-runtime.js";

const here = dirname(fileURLToPath(import.meta.url));
const fixture = JSON.parse(await readFile(
  resolve(here, "../../openneko/internal/packs/testdata/magento_runtime_parity.json"),
  "utf8",
)) as {
  reconciliation: Array<{ name: string; operation: Record<string, unknown>; reconciled: Record<string, unknown> | null; expected: unknown; confirmed: boolean }>;
  mutationField: Array<{ root: string; field?: string; error?: string }>;
  terminalBulk: Array<{ status: number; terminal: boolean }>;
};

describe("Magento runtime parity fixture shared with the Go runtime", () => {
  it.each(fixture.reconciliation)("reconciliation: $name", (item) => {
    const operation = { mutationRoot: "", defaultClass: 2, reversible: true, ...item.operation } as Parameters<typeof reconciliationConfirmed>[0];
    expect(reconciliationConfirmed(operation, item.reconciled, item.expected)).toBe(item.confirmed);
  });

  it.each(fixture.mutationField)("mutation field: $root", (item) => {
    if (item.error) expect(() => magentoGraphjinMutationField(item.root)).toThrow(item.error);
    else expect(magentoGraphjinMutationField(item.root)).toBe(item.field);
  });

  it.each(fixture.terminalBulk)("bulk status $status", (item) => {
    expect(isTerminalMagentoBulkStatus(item.status)).toBe(item.terminal);
  });
});
