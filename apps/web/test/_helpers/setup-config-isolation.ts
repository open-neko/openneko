/**
 * Per-file config-dir isolation for the suite.
 */
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

process.env.XDG_CONFIG_HOME = mkdtempSync(join(tmpdir(), "neko-test-config-"));
