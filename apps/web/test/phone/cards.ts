import { ANSWER_FIXTURES } from "../../src/app/dev/answer-preview/fixtures";

export const cardThreads = Object.keys(ANSWER_FIXTURES).map((key, index) => {
  const n = String(index + 1).padStart(2, "0");
  return {
    key,
    threadId: `c0a7ac70-0000-4000-8000-0000000c10${n}`,
    runId: `c0a7ac70-0000-4000-8000-0000000c20${n}`,
    messages: ANSWER_FIXTURES[key]!.messages,
  };
});
