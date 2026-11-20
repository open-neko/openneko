import { describe, expect, it } from "vitest";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import EgressHostsField, { egressHostLines } from "@/app/admin/settings/agent/EgressHostsField";

function render(props: Partial<Parameters<typeof EgressHostsField>[0]> = {}) {
  return renderToStaticMarkup(
    createElement(EgressHostsField, {
      value: "",
      onChange: () => {},
      suggestions: [],
      available: true,
      ...props,
    }),
  );
}

describe("EgressHostsField", () => {
  it("reads one host per line or comma and drops blanks and repeats", () => {
    expect(egressHostLines(" pypi.org\n\nfiles.example.com:8443, pypi.org\n*.example.com ")).toEqual([
      "pypi.org",
      "files.example.com:8443",
      "*.example.com",
    ]);
    expect(egressHostLines("")).toEqual([]);
  });

  it("offers each pack host that is not listed yet", () => {
    const html = render({ value: "api.resend.com", suggestions: ["api.resend.com", "slack.com"] });
    expect(html).toContain("Add slack.com");
    expect(html).not.toContain("Add api.resend.com");
    expect(html).toContain("An empty list means no internet.");
  });

  it("is off without a sandbox", () => {
    const html = render({ available: false, suggestions: ["slack.com"] });
    expect(html).toContain("disabled");
    expect(html).not.toContain("Add slack.com");
    expect(html).toContain("this install has no sandbox service");
  });
});
