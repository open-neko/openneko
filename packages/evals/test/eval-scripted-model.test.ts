import { describe, expect, it } from "vitest";
import { BACKGROUND_RUN_ANSWER, backgroundRunReply } from "../scripts/eval-scripted-model";

const headless = "<role>runner</role>\n<mode>headless</mode>";
const tool = [{ type: "function", function: { name: "x" } }];

describe("scripted model background runs", () => {
  it("ends a headless workflow run without the armed turn", () => {
    expect(backgroundRunReply({ messages: [{ role: "system", content: `## Distiller\n${headless}` }], tools: tool } as never)).toEqual({
      status: 200,
      content: JSON.stringify({ javascriptCode: "final('Report the scripted answer', {})" }),
    });
    expect(backgroundRunReply({ messages: [{ role: "system", content: headless }], tools: tool } as never)?.content).toContain("final(");
    expect(backgroundRunReply({ messages: [{ role: "user", content: [{ type: "text", text: headless }] }] } as never)).toEqual({
      status: 200,
      content: `Answer: ${BACKGROUND_RUN_ANSWER}`,
    });
  });

  it("leaves a member turn to the armed program", () => {
    expect(backgroundRunReply({ messages: [{ role: "system", content: "<mode>live</mode>" }], tools: tool } as never)).toBeUndefined();
    expect(backgroundRunReply({ messages: [{ role: "user", content: "Watch the eval metric." }] } as never)).toBeUndefined();
  });
});
