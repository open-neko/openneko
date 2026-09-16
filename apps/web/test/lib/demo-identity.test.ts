import { afterEach, describe, expect, it, vi } from "vitest";
import { demoSafeEmail, demoSafeName } from "@/lib/demo-mode";

afterEach(() => vi.unstubAllEnvs());

describe("demo identity", () => {
  it("hides an address a visitor typed into the public demo", () => {
    vi.stubEnv("NEXT_PUBLIC_DEMO", "true");
    const masked = demoSafeEmail("someone.real@gmail.com", "usr_AbC123xyz");
    expect(masked).not.toContain("someone.real");
    expect(masked).toBe("operator-3xyz@example.com");
    expect(demoSafeName("Someone Real")).toBe("Demo operator");
  });

  it("keeps each account distinguishable without naming anyone", () => {
    vi.stubEnv("DEMO", "true");
    expect(demoSafeEmail("a@x.com", "usr_1111")).not.toBe(demoSafeEmail("b@x.com", "usr_2222"));
    expect(demoSafeEmail("a@x.com", "")).toBe("operator@example.com");
  });

  it("leaves a real installation alone", () => {
    expect(demoSafeEmail("amit@buddhic.ai", "usr_1")).toBe("amit@buddhic.ai");
    expect(demoSafeName("Amit")).toBe("Amit");
    expect(demoSafeEmail("", "usr_1")).toBe("");
  });
});
