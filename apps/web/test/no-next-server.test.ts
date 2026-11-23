import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const web = (rel: string) => fileURLToPath(new URL(`../${rel}`, import.meta.url));

describe("the static app", () => {
  const pkg = JSON.parse(readFileSync(web("package.json"), "utf8")) as {
    dependencies: Record<string, string>;
    devDependencies: Record<string, string>;
  };

  it("does not depend on the Next server", () => {
    expect(pkg.dependencies).not.toHaveProperty("next");
    expect(pkg.devDependencies).not.toHaveProperty("next");
    expect(existsSync(web("pnpm-lock.yaml"))).toBe(false);
  });

  it("keeps the packages that only tests use out of the app dependencies", () => {
    for (const name of ["pg", "yaml"]) {
      expect(pkg.dependencies).not.toHaveProperty(name);
      expect(pkg.devDependencies).toHaveProperty(name);
    }
  });
});
