import { describe, expect, it } from "vitest";
import { recordsGraphjinConnectionString } from "../../src/records/schema-runtime";

describe("recordsGraphjinConnectionString", () => {
  const env = {
    RECORDS_PG_HOST: "127.0.0.1",
    RECORDS_PG_PORT: "5434",
    RECORDS_PG_USER: "records",
    RECORDS_PG_PASSWORD: "secret",
    RECORDS_PG_DATABASE: "records",
    OPENNEKO_PG_ENV_OVERRIDE: "1",
  };

  it("uses the worker's records database address by default", () => {
    expect(recordsGraphjinConnectionString(env)).toBe("postgresql://records:secret@127.0.0.1:5434/records");
  });

  it("gives records GraphJin its own address when one is set", () => {
    expect(
      recordsGraphjinConnectionString({ ...env, OPENNEKO_RECORDS_GRAPHJIN_DB_HOST: "records-db", OPENNEKO_RECORDS_GRAPHJIN_DB_PORT: "5432" }),
    ).toBe("postgresql://records:secret@records-db:5432/records");
  });
});
