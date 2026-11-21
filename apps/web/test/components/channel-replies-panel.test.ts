import { describe, expect, it } from "vitest";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { ReplyRuleForm, placeLines, replyRuleLine } from "@/app/admin/settings/channels/ChannelRepliesPanel";

function render(props: Partial<Parameters<typeof ReplyRuleForm>[0]> = {}) {
  return renderToStaticMarkup(
    createElement(ReplyRuleForm, {
      channel: "slack",
      rule: { mode: "listed", places: ["C0123ABCD"] },
      keptFromUpgrade: false,
      onSaved: () => {},
      ...props,
    }),
  );
}

describe("ChannelRepliesPanel", () => {
  it("reads one chat ID per line or comma and drops blanks and repeats", () => {
    expect(placeLines(" C01\n\nC02, C01\n")).toEqual(["C01", "C02"]);
    expect(placeLines("")).toEqual([]);
  });

  it("explains the kept thread answer after an upgrade in one line", () => {
    const html = render({ rule: { mode: "thread", places: [] }, keptFromUpgrade: true });
    expect(html).toContain("OpenNeko answered in the thread before this upgrade, so it still does.");
    expect(html).not.toContain("Channels that get answers in the thread");
    expect(replyRuleLine("slack", "thread", false)).toBe("In the thread. Everyone in the channel can read the answer.");
  });

  it("shows the list only in listed mode, with the stored IDs", () => {
    const html = render();
    expect(html).toContain("Channels that get answers in the thread");
    expect(html).toContain("C0123ABCD");
    expect(html).toContain("By direct message in every other channel.");
  });

  it("names the group and the private chat for Telegram", () => {
    const html = render({ channel: "telegram", rule: { mode: "direct", places: [] } });
    expect(html).toContain("In a private chat with the person who asked. The group gets a one-line note with no data.");
    expect(html).toContain("Always in the group");
  });
});
