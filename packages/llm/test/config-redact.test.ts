import { describe, expect, it } from "vitest";
import { redactSourceConfig } from "../src/graphjin/config-redact";

describe("redactSourceConfig", () => {
  it("hides secret keys and URL credentials and keeps the rest", () => {
    const redacted = redactSourceConfig({
      sources: [
        {
          name: "shop",
          host: "db.internal",
          Password: "a",
          "api-key": "b",
          connection_string: "c",
          DSN: "d",
          token_from_request: { header: "x" },
          redis: { URL: "redis://user:pw@cache:6379/0" },
          specs_dir: "/config/specs",
        },
      ],
      blocklist: ["secrets_table"],
    });
    expect(redacted).toEqual({
      sources: [
        {
          name: "shop",
          host: "db.internal",
          Password: "[REDACTED]",
          "api-key": "[REDACTED]",
          connection_string: "[REDACTED]",
          DSN: "[REDACTED]",
          token_from_request: "[REDACTED]",
          redis: { URL: "redis://[REDACTED]@cache:6379/0" },
          specs_dir: "/config/specs",
        },
      ],
      blocklist: ["secrets_table"],
    });
  });
});
