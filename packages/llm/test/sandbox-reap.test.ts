import { describe, expect, it } from "vitest";
import { reapStrandedSandboxes, sandboxOwnerLabelArgs } from "../src/work/sandbox-launcher";

describe("stranded sandbox cleanup", () => {
  it("labels boxes with the owning host and its boot", () => {
    expect(sandboxOwnerLabelArgs("worker", "boot-2")).toEqual(["--label", "openneko.owner=worker", "--label", "openneko.boot=boot-2"]);
  });

  it("deletes only this host's boxes from earlier boots", async () => {
    const calls: string[][] = [];
    const run = async (args: string[]) => {
      calls.push(args);
      if (args[1] === "list") {
        return JSON.stringify([
          { name: "warm-old", labels: { "openneko.owner": "worker", "openneko.boot": "boot-1" } },
          { name: "job-old", labels: { "openneko.owner": "worker", "openneko.boot": "boot-1" } },
          { name: "warm-live", labels: { "openneko.owner": "worker", "openneko.boot": "boot-2" } },
        ]);
      }
      if (args[2] === "job-old") throw new Error("sandbox not found");
      return "";
    };
    expect(await reapStrandedSandboxes(run, "worker", "boot-2")).toEqual(["warm-old", "job-old"]);
    expect(calls[0]).toEqual(["sandbox", "list", "--selector", "openneko.owner=worker", "-o", "json", "--limit", "500"]);
    expect(calls.slice(1)).toEqual([["sandbox", "delete", "warm-old"], ["sandbox", "delete", "job-old"]]);
  });
});
