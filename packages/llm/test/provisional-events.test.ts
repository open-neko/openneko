import {expect, it} from "vitest";
import {parseProvisionalNotification, provisionalNotifications} from "../src/work/provisional-events";

it("bounds live notifications without splitting UTF-8 code points or crossing runs", () => {
  const source = "🙂".repeat(1025);
  const payloads = provisionalNotifications("org-one", "run-one", {type: "provisional_answer", version: 1, index: 0, text: source});
  expect(payloads.length).toBeGreaterThan(1);
  expect(payloads.every((payload) => Buffer.byteLength(payload, "utf8") < 4096)).toBe(true);
  const parsed = payloads.map(parseProvisionalNotification);
  expect(parsed.every(Boolean)).toBe(true);
  expect(parsed.map((item) => item?.event.text).join("")).toBe(source);
  expect(parsed.every((item) => item?.orgId === "org-one" && item.runId === "run-one")).toBe(true);
  expect(parseProvisionalNotification(JSON.stringify({orgId: "other", runId: "run-one", event: {type: "message", content: "forged"}}))).toBeUndefined();
});
