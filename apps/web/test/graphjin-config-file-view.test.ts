import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { ConfigFileView } from "../src/app/admin/settings/graphjin/ConfigFileView";
import { ConfigDescription } from "../src/app/admin/settings/graphjin/ConfigDescription";

function render(node: ReturnType<typeof createElement>) {
  return renderToStaticMarkup(createElement("div", null, node));
}

it("shows the admin the path, the state and the YAML, read-only", () => {
  const html = render(
    createElement(ConfigFileView, {
      view: {
        path: "/etc/openneko/agentic.yml",
        yaml: "databases:\n  shop:\n    host: db.internal\n    password: '[REDACTED]'\n",
        error: null,
        state: { state: "running", reason: null, since: "2026-11-11T08:00:00Z", restarts: 0 },
      },
    }),
  );
  expect(html).toContain("/etc/openneko/agentic.yml");
  expect(html).toContain("GraphJin is running.");
  expect(html).toContain("openneko graphjin check");
  expect(html).toContain("host: db.internal");
  expect(html).not.toMatch(/<(input|textarea|form)\b/);
});

it("shows the file error and the restart reason", () => {
  const html = render(
    createElement(ConfigFileView, {
      view: {
        path: "/etc/openneko/agentic.yml",
        yaml: null,
        error: "the file does not exist",
        state: { state: "restarting", reason: "dial failed", since: null, restarts: 2 },
      },
    }),
  );
  expect(html).toContain("OpenNeko could not show the file: the file does not exist.");
  expect(html).toContain("GraphJin is restarting: dial failed");
});

it("describes each source and names a source that failed", () => {
  const html = render(
    createElement(ConfigDescription, {
      descriptions: [
        { source: { id: "1", name: "down" }, error: "connection refused", summary: null },
        {
          source: { id: "2", name: "erp" },
          error: null,
          summary: { sources: [{ name: "erp", kind: "database", sentences: ["A PostgreSQL source."] }], roles: [] },
        },
      ],
    }),
  );
  expect(html).toContain("OpenNeko could not read this config: connection refused");
  expect(html).toContain("Database · erp");
  expect(html).toContain("A PostgreSQL source.");
});
