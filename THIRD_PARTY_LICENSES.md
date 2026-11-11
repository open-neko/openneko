# Third-party software shipped with OpenNeko

The source code in this repository is distributed under the
[Apache License 2.0](LICENSE) unless a file clearly states different terms.
This file lists third-party software that ships with OpenNeko, and the
additional license obligations it carries.

This file complements [NOTICE](NOTICE). Operators who redistribute OpenNeko in
any form should ship LICENSE, NOTICE, this file, and any applicable third-party
license texts alongside their distribution.

---

## Modular agents and skills

OpenNeko can invoke or interoperate with external agents and skills.
Those components remain under their own licenses.

| Component | License | Notes |
|---|---|---|
| Built-in skills with `license:` frontmatter | As declared in each skill | Check each `SKILL.md` and local `LICENSE.txt`. |

---

## Sandbox skill packages

The sandbox recipe image installs these npm packages from
`recipes/sandbox/skills/package-lock.json`. The bundled `xlsx` and `docx`
skills use them. The OpenNeko images do not carry them.

| Package | Version | License | Notes |
|---|---|---|---|
| `fflate` | 0.8.3 | MIT | Reads and writes the zip container of a workbook or document |
| `fast-xml-parser` | 5.12.0 | MIT | Reads and writes workbook and document XML |
| `fast-xml-builder`, `strnum`, `anynum`, `path-expression-matcher`, `xml-naming`, `is-unsafe`, `@nodable/entities` | as locked | MIT | Dependencies of `fast-xml-parser` |
| `fast-formula-parser` | 1.0.19 | MIT | Calculates formulas; holds a copy of SheetJS SSF (Apache-2.0) to format numbers |
| `chevrotain`, `regexp-to-ast` | 7.1.2, 0.5.0 | Apache-2.0, MIT | Parser toolkit of `fast-formula-parser` |
| `bahttext` | 1.1.0 | MIT | Dependency of `fast-formula-parser` |
| `@formulajs/formulajs` | 4.6.1 | MIT | Calculates the functions that `fast-formula-parser` lacks |
| `bessel` | 1.0.2 | Apache-2.0 | Engineering functions for both formula packages |
| `jstat` | 1.9.6 | MIT | Statistics functions for both formula packages |

---

## Other notable dependencies

OpenNeko's runtime dependencies are listed in the `package.json` and `go.mod` files across the monorepo. The substantial ones with non-Apache-2.0 licenses:

| Package | License | Notes |
|---|---|---|
| `pg`, `pg-boss` | MIT | Postgres client + job queue |
| `mysql2` | MIT | MySQL and MariaDB client for the Magento pack and the source check |
| `next` | MIT | Web app framework |
| `zod` | MIT | Schema validation |
| `drizzle-orm` | Apache-2.0 | DB schema + queries |
| DuckDB CLI | MIT | Shipped in the `neko-serve` image; runs SQL over sheet tables |
| `github.com/xuri/excelize/v2` | BSD-3-Clause | Reads Excel files in `openneko serve` |
| `github.com/ledongthuc/pdf` | BSD-3-Clause | Reads PDF text in `openneko serve` |
| `github.com/parquet-go/parquet-go` | Apache-2.0 | Writes sheet tables as Parquet |
| `github.com/cenkalti/backoff/v5` | MIT | Retries in the OTLP trace exporter of `openneko serve` |
| `github.com/grpc-ecosystem/grpc-gateway/v2` | BSD-3-Clause | Linked by the OTLP trace exporter of `openneko serve` |

Run `pnpm licenses ls -P` from the repo root to see the full transitive list at a given snapshot.

---

## Reporting

Discrepancies, missing attributions, or compliance questions: https://github.com/open-neko/openneko/issues.
