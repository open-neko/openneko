import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { PRIMARY_PROVIDER_OPTIONS, getDefaultPrimaryModel, getPrimaryProviderFields } from "../src/config";
import { MODEL_TASKS } from "../src/model-tasks";

describe("model catalogue fixture", () => {
  it("matches the provider list, defaults, fields and tasks that Go also reads", () => {
    const fixture = JSON.parse(readFileSync(new URL("./fixtures/model-catalog.json", import.meta.url), "utf8"));
    expect({
      providers: PRIMARY_PROVIDER_OPTIONS,
      defaults: Object.fromEntries(PRIMARY_PROVIDER_OPTIONS.map((o) => [o.value, getDefaultPrimaryModel(o.value)])),
      fields: Object.fromEntries(PRIMARY_PROVIDER_OPTIONS.map((o) => [o.value, getPrimaryProviderFields(o.value)])),
      tasks: MODEL_TASKS,
    }).toEqual(fixture);
  });
});
