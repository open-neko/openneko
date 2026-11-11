# Third-party software shipped with OpenNeko

The source code in this repository is distributed under the
[Apache License 2.0](LICENSE) unless a file clearly states different terms.
This file lists third-party software that ships with OpenNeko, and the
additional license obligations it carries.

This file complements [NOTICE](NOTICE). Operators who redistribute OpenNeko in
any form should ship LICENSE, NOTICE, this file, and any applicable third-party
license texts alongside their distribution.

---

## Bundled skills

Every bundled skill and every skill in a bundled solution pack is Apache-2.0.

| Component | License | Notes |
|---|---|---|
| `skill-creator` skill | Apache-2.0 | Authored by Anthropic. Keep its `LICENSE.txt` and attribution. |

---

## Sandbox skill packages

The sandbox recipe image installs these npm packages from
`recipes/sandbox/skills/package-lock.json`. The bundled `xlsx`, `docx`,
`pptx` and `pdf` skills use them. The OpenNeko images do not carry them.

| Package | Version | License | Notes |
|---|---|---|---|
| `fflate` | 0.8.3 | MIT | Reads and writes the zip container of a workbook, document or deck |
| `fast-xml-parser` | 5.12.0 | MIT | Reads and writes workbook, document and deck XML |
| `fast-xml-builder`, `strnum`, `anynum`, `path-expression-matcher`, `xml-naming`, `is-unsafe`, `@nodable/entities` | as locked | MIT | Dependencies of `fast-xml-parser` |
| `fast-formula-parser` | 1.0.19 | MIT | Calculates formulas; holds a copy of SheetJS SSF (Apache-2.0) to format numbers |
| `chevrotain`, `regexp-to-ast` | 7.1.2, 0.5.0 | Apache-2.0, MIT | Parser toolkit of `fast-formula-parser` |
| `bahttext` | 1.1.0 | MIT | Dependency of `fast-formula-parser` |
| `@formulajs/formulajs` | 4.6.1 | MIT | Calculates the functions that `fast-formula-parser` lacks |
| `bessel` | 1.0.2 | Apache-2.0 | Engineering functions for both formula packages |
| `jstat` | 1.9.6 | MIT | Statistics functions for both formula packages |
| `pdf-lib` | 1.17.1 | MIT | Creates and changes PDF files, pages and forms |
| `@pdf-lib/fontkit` | 1.1.1 | MIT | Embeds TrueType and OpenType fonts in a PDF |
| `@pdf-lib/standard-fonts`, `@pdf-lib/upng` | 1.0.0, 1.0.1 | MIT | Dependencies of `pdf-lib` |
| `pako` | 1.0.11 | MIT AND Zlib | Compresses PDF streams for `pdf-lib`; holds a port of zlib under the Zlib license |
| `tslib` | 1.14.1 | 0BSD | Runtime helpers of `pdf-lib` |
| `pdfjs-dist` | 6.5.438 | Apache-2.0 | Reads PDF text, form fields and bookmarks. It carries Adobe CMap files (BSD-3-Clause), which the skill uses to read CJK text, and Foxit and Liberation fonts (BSD-style and SIL OFL 1.1), which the skill does not use |
| `@napi-rs/canvas` | 1.1.0 | MIT | Optional package of `pdfjs-dist`. The CI test run installs it; the sandbox image leaves it out |

The sandbox recipe image also installs two Debian font packages. The `pdf`
skill embeds a subset of them in a PDF when its built-in fonts cannot draw a
character. The image keeps only the faces in the table.

| Package | Version | License | Faces kept |
|---|---|---|---|
| `fonts-dejavu-core` | 2.37-6 | Bitstream Vera license; DejaVu changes are public domain | DejaVu Sans, DejaVu Sans Bold, DejaVu Sans Mono. They draw ₹, arrows, Greek and Cyrillic |
| `fonts-droid-fallback` | 6.0.1r16-1.1 | Apache-2.0 | Droid Sans Fallback. It draws Chinese, Japanese and Korean |

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
