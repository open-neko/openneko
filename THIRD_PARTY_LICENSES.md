# Third-party software shipped with OpenNeko

OpenNeko is distributed under the [Apache License 2.0](LICENSE). The Apache-2.0 grant covers OpenNeko's own source code. This file lists third-party software that is **shipped alongside** OpenNeko (bundled in npm dependencies, Docker images, or release artifacts) and the additional license obligations those carry.

This file is the canonical NOTICE. Operators who redistribute OpenNeko in any form must ship this file alongside.

---

## Plugin runtime — microsandbox

When the plugin subsystem is enabled (microsandbox is installed and the host supports it), OpenNeko ships three layers of licensed components:

### 1. `microsandbox` (npm) and `containers/libkrun`

| Component | License | Source |
|---|---|---|
| [`microsandbox`](https://www.npmjs.com/package/microsandbox) (npm wrapper) | Apache-2.0 | https://github.com/superradcompany/microsandbox |
| `@superradcompany/microsandbox-{darwin-arm64,linux-x64-gnu,linux-arm64-gnu}` (platform bundles) | Apache-2.0 | https://github.com/superradcompany/microsandbox |
| [`containers/libkrun`](https://github.com/containers/libkrun) (statically linked into the platform bundle) | Apache-2.0 | https://github.com/containers/libkrun |

These components are governed by the Apache License 2.0. The terms in `LICENSE` apply.

### 2. `containers/libkrunfw` — LGPL-2.1-only shim

`libkrunfw` is a shim that loads a Linux kernel image inside the microVM. It is shipped as an unmodified `.dylib`/`.so` inside the platform package and is dynamically loaded at runtime.

- **License:** LGPL-2.1-only — https://github.com/containers/libkrunfw
- **Obligation:** §6(a) of the LGPL is satisfied by shipping the unmodified shared library. OpenNeko does not modify libkrunfw; the binary inside `@superradcompany/microsandbox-<triple>/lib/` is the upstream release.
- **License text:** https://www.gnu.org/licenses/old-licenses/lgpl-2.1.txt

If you redistribute OpenNeko or the microsandbox bundle, you must include the full LGPL-2.1 text and either (a) the corresponding shared-library object form (which the platform package already provides), or (b) a written offer to provide it.

### 3. Linux kernel image inside libkrunfw — GPL-2.0-only

`libkrunfw` bundles a Linux kernel image. The kernel is **GPL-2.0-only** and triggers the kernel's "corresponding source" obligation if you redistribute the bundle.

- **License:** GPL-2.0-only — https://www.kernel.org/doc/html/latest/process/license-rules.html
- **License text:** https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt
- **Kernel version:** the exact version shipped by libkrunfw is documented in libkrunfw's release notes. See https://github.com/containers/libkrunfw/releases for the corresponding tag.

**Written offer for the kernel source.** OpenNeko relies on libkrunfw's upstream release; we do not modify the kernel. If you require the corresponding kernel source for the version shipped in your OpenNeko build, retrieve it from kernel.org at the version libkrunfw documents for the release you have installed, or open an issue at https://github.com/open-neko/neko/issues and we will direct you to the matching commit.

---

## Other notable dependencies

OpenNeko's runtime dependencies are listed in the various `package.json` files across the monorepo. The substantial ones with non-Apache-2.0 licenses:

| Package | License | Notes |
|---|---|---|
| `pg`, `pg-boss` | MIT | Postgres client + job queue |
| `next` | MIT | Web app framework |
| `@huggingface/transformers` | Apache-2.0 | Embedding model runtime |
| `zod` | MIT | Schema validation |
| `drizzle-orm` | Apache-2.0 | DB schema + queries |

Run `pnpm licenses ls -P` from the repo root to see the full transitive list at a given snapshot.

---

## Reporting

Discrepancies, missing attributions, or compliance questions: https://github.com/open-neko/neko/issues.
