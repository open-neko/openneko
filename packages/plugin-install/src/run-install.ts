// Install orchestration. Used by:
//   - the openneko CLI (operator-driven; CLI supplies the trusted-
//     marketplaces list from its own marketplace-store and the
//     envPrompt that wraps a hidden-input TTY prompt)
//   - future server-side install path (when the agent emits an
//     install_plugin action request — the worker supplies trusted
//     marketplaces from its own state and the envPrompt comes from a
//     web-UI modal)
//
// This module intentionally takes `trustedMarketplaces` as an input
// rather than reading a store internally — keeps it decoupled from
// the CLI's per-user marketplace-trust file, which the worker
// doesn't need.
import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { cp, mkdir, readFile } from "node:fs/promises";
import { homedir } from "node:os";
import path from "node:path";
import {
  emptyManifest,
  readManifest,
  upsertEntry,
  writeManifest,
  type ManifestEntry,
} from "./manifest.js";
import {
  createMarketplaceClient,
  findPlugin,
  pickInstallVersion,
  type Marketplace,
  type MarketplaceClient,
  type MarketplaceEnvRequirement,
  type MarketplaceVersion,
} from "./marketplace-client.js";
import {
  listKeysForPlugin,
  readSecretsStore,
  setSecret,
  writeSecretsStore,
} from "./secrets-store.js";

export interface TrustedMarketplace {
  name: string;
  url: string;
}

export interface InstallOptions {
  repoRoot: string;
  /** Spec is `<pluginName>` or `<pluginName>@<marketplace-name-or-url>`. */
  spec: string;
  version?: string;
  unverified?: boolean;
  /** Marketplaces the caller already trusts. Ignored when unverified. */
  trustedMarketplaces: TrustedMarketplace[];
  /** Override config dir for the secrets store. */
  secretsConfigDir?: string;
  /**
   * Override the root the bundled-skill half copies into. Default
   * `~/.openneko/skills/`. The worker's skill loader (M8) walks this
   * dir to find community skills.
   */
  skillsInstallDir?: string;
  marketplaceClient?: MarketplaceClient;
  /** For tests: skip the npm subprocess call. */
  npmRunner?: (args: string[], cwd: string) => Promise<void>;
  /**
   * Resolve the value of an env requirement during install. Caller
   * supplies the prompt (TTY-hidden for the CLI; web-UI modal for the
   * server). No default — without this, the library would have to
   * assume a UX layer.
   */
  envPrompt: (
    plugin: string,
    requirement: MarketplaceEnvRequirement,
  ) => Promise<string>;
  /**
   * Policy values in effect when this install runs. Recorded verbatim
   * on the new manifest entry's policySnapshot so audit can answer
   * "how did this get installed?" months later. Omit to skip the
   * snapshot (pre-feature compatibility; tests).
   */
  policySnapshot?: {
    allowUnverified: boolean;
    allowGitUrlInstalls: boolean;
    allowSandboxedSkillEscape: boolean;
    allowedMarketplaces: string[];
  };
}

export interface InstallResult {
  name: string;
  version: string;
  integrity: string;
  permissions: { network: string[] };
  marketplace: string | null;
  source: "marketplace" | "unverified" | "git-url";
  /** Required env keys that were prompted for and saved during this install. */
  envSaved: string[];
  /** Required env keys that were already present in the secrets store. */
  envAlreadySet: string[];
  /** ISO timestamp recorded on the manifest entry. */
  installedAt: string;
  /**
   * If the installed package bundled a skill half (openneko.skill in
   * package.json), this is the absolute path it was copied to under
   * ~/.openneko/skills/<skill-name>/. Absent when the package only
   * contributed a plugin half.
   */
  skillInstalledAt?: string;
}

export interface ParsedSpec {
  name: string;
  marketplaceRef: string | null;
}

export function parseInstallSpec(spec: string): ParsedSpec {
  if (!spec) throw new Error("install: package name required");
  const at = spec.lastIndexOf("@");
  if (at <= 0) return { name: spec, marketplaceRef: null };
  const name = spec.slice(0, at);
  const marketplaceRef = spec.slice(at + 1);
  if (!marketplaceRef) return { name: spec, marketplaceRef: null };
  return { name, marketplaceRef };
}

export async function runInstall(
  options: InstallOptions,
): Promise<InstallResult> {
  if (options.unverified) return installUnverified(options);

  const parsed = parseInstallSpec(options.spec);
  const client = options.marketplaceClient ?? createMarketplaceClient();

  let targets = options.trustedMarketplaces;
  if (parsed.marketplaceRef) {
    const match = options.trustedMarketplaces.find(
      (m) => m.name === parsed.marketplaceRef || m.url === parsed.marketplaceRef,
    );
    if (!match) {
      throw new Error(
        `install: marketplace "${parsed.marketplaceRef}" not trusted — add it first with \`openneko marketplace add <url>\``,
      );
    }
    targets = [match];
  }

  const hits: Array<{
    marketplaceName: string;
    marketplaceUrl: string;
    marketplace: Marketplace;
  }> = [];
  for (const m of targets) {
    let marketplace: Marketplace;
    try {
      marketplace = await client.fetch(m.url);
    } catch (err) {
      throw new Error(
        `install: failed to fetch marketplace ${m.name} (${m.url}): ${err instanceof Error ? err.message : err}`,
      );
    }
    if (findPlugin(marketplace, parsed.name)) {
      hits.push({
        marketplaceName: m.name,
        marketplaceUrl: m.url,
        marketplace,
      });
    }
  }

  if (hits.length === 0) {
    throw new Error(
      `install: plugin "${parsed.name}" not found in any trusted marketplace (${targets.map((m) => m.name).join(", ")})`,
    );
  }
  if (hits.length > 1) {
    const choices = hits.map((h) => `${parsed.name}@${h.marketplaceName}`);
    throw new Error(
      `install: plugin "${parsed.name}" is listed in multiple trusted marketplaces. Pick one:\n  ${choices.join("\n  ")}`,
    );
  }

  const chosen = hits[0]!;
  const plugin = findPlugin(chosen.marketplace, parsed.name)!;
  const version = pickInstallVersion(plugin, options.version);

  // Resolve any required env BEFORE the npm install runs, so we don't
  // leave a half-installed plugin if the operator can't supply a key.
  const envOutcome = await resolveRequiredEnv(plugin.name, version, options);

  const npmRunner = options.npmRunner ?? runNpm;
  await npmRunner(
    ["install", `${plugin.name}@${version.version}`],
    options.repoRoot,
  );

  const manifest = (await readManifest(options.repoRoot)) ?? emptyManifest();
  const installedAt = new Date().toISOString();
  const entry: ManifestEntry = {
    name: plugin.name,
    version: version.version,
    integrity: version.integrity,
    permissions: {
      network: version.permissions?.network ?? [],
      env: version.permissions?.env ?? [],
    },
    capabilities: version.capabilities,
    marketplace: chosen.marketplaceName,
    installSource: "marketplace",
    installedAt,
    policySnapshot: options.policySnapshot ?? null,
  };
  await writeManifest(options.repoRoot, upsertEntry(manifest, entry));
  const skillInstalledAt = await copyBundledSkill(
    plugin.name,
    options.repoRoot,
    options.skillsInstallDir,
  );
  return {
    name: plugin.name,
    version: version.version,
    integrity: version.integrity,
    permissions: { network: entry.permissions.network },
    marketplace: chosen.marketplaceName,
    source: "marketplace",
    envSaved: envOutcome.saved,
    envAlreadySet: envOutcome.alreadySet,
    installedAt,
    ...(skillInstalledAt ? { skillInstalledAt } : {}),
  };
}

async function resolveRequiredEnv(
  pluginName: string,
  version: MarketplaceVersion,
  options: InstallOptions,
): Promise<{ saved: string[]; alreadySet: string[] }> {
  const required = (version.permissions?.env ?? []).filter(
    (r) => r.required !== false,
  );
  if (required.length === 0) return { saved: [], alreadySet: [] };

  const store = await readSecretsStore(options.secretsConfigDir);
  const existing = new Set(listKeysForPlugin(store, pluginName));
  const alreadySet = required.filter((r) => existing.has(r.key)).map((r) => r.key);
  const missing = required.filter((r) => !existing.has(r.key));
  if (missing.length === 0) return { saved: [], alreadySet };

  let updated = store;
  const saved: string[] = [];
  for (const req of missing) {
    const value = await options.envPrompt(pluginName, req);
    if (!value) {
      throw new Error(
        `install: required env "${req.key}" not supplied for ${pluginName}`,
      );
    }
    updated = setSecret(updated, pluginName, req.key, value);
    saved.push(req.key);
  }
  await writeSecretsStore(updated, options.secretsConfigDir);
  return { saved, alreadySet };
}

async function installUnverified(
  options: InstallOptions,
): Promise<InstallResult> {
  const parsed = parseInstallSpec(options.spec);
  const name = parsed.name;
  const npmRunner = options.npmRunner ?? runNpm;
  const spec = options.version ? `${name}@${options.version}` : name;
  await npmRunner(["install", spec], options.repoRoot);

  const meta = await readPackageMeta(name, options.repoRoot);
  if (!meta) {
    throw new Error(
      `--unverified install: cannot read package.json for ${name} after install`,
    );
  }
  const ozNeko = meta.openneko;
  if (!ozNeko?.capabilities) {
    throw new Error(
      `--unverified install: ${name} package.json must declare openneko.capabilities`,
    );
  }
  const installedAt = new Date().toISOString();
  const entry: ManifestEntry = {
    name,
    version: meta.version,
    integrity: meta._integrity ?? "sha512-unknown",
    permissions: {
      network: ozNeko.permissions?.network ?? [],
      env: ozNeko.permissions?.env ?? [],
    },
    capabilities: ozNeko.capabilities,
    installSource: "unverified",
    installedAt,
    policySnapshot: options.policySnapshot ?? null,
  };
  const manifest = (await readManifest(options.repoRoot)) ?? emptyManifest();
  await writeManifest(options.repoRoot, upsertEntry(manifest, entry));
  const skillInstalledAt = await copyBundledSkill(
    name,
    options.repoRoot,
    options.skillsInstallDir,
  );
  return {
    name,
    version: entry.version,
    integrity: entry.integrity,
    permissions: { network: entry.permissions.network },
    marketplace: null,
    source: "unverified",
    envSaved: [],
    envAlreadySet: [],
    installedAt,
    ...(skillInstalledAt ? { skillInstalledAt } : {}),
  };
}

interface NpmPackageMeta {
  version: string;
  _integrity?: string;
  openneko?: {
    /** Path to the runner entrypoint, relative to the package root. */
    runner?: string;
    /**
     * Path to a bundled SKILL folder, relative to the package root.
     * When present, install copies it under ~/.openneko/skills/<name>/
     * after the plugin npm install completes.
     */
    skill?: string;
    permissions?: {
      network?: string[];
      env?: Array<{
        key: string;
        required?: boolean;
        secret?: boolean;
        description: string;
      }>;
    };
    capabilities?: {
      action?: { kinds: Array<{ kind: string; description: string }> };
      auth?: { providerLabel?: string };
      connect?: {
        providerLabel: string;
        scopes: string[];
        flow?: "oauth2-pkce";
      };
    };
  };
}

async function readPackageMeta(
  name: string,
  cwd: string,
): Promise<NpmPackageMeta | null> {
  const { readFile } = await import("node:fs/promises");
  const path = await import("node:path");
  try {
    const file = path.join(cwd, "node_modules", name, "package.json");
    return JSON.parse(await readFile(file, "utf8")) as NpmPackageMeta;
  } catch {
    return null;
  }
}

function runNpm(args: string[], cwd: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const child = spawn("npm", args, { cwd, stdio: "inherit" });
    child.on("error", reject);
    child.on("close", (code) => {
      if (code === 0) resolve();
      else reject(new Error(`npm ${args.join(" ")} exited ${code}`));
    });
  });
}

/**
 * If the just-installed package declares `openneko.skill` in its
 * package.json, copy the skill folder under ~/.openneko/skills/<skill-name>/
 * so the worker's skill loader (M8) can pick it up. The skill's name is
 * taken from its SKILL.md frontmatter; falls back to the package's
 * unscoped basename when the SKILL.md can't be parsed.
 *
 * Returns the absolute destination path on success, or null when the
 * package declared no skill half (most plugins).
 */
async function copyBundledSkill(
  pluginName: string,
  repoRoot: string,
  skillsInstallDir?: string,
): Promise<string | null> {
  const meta = await readPackageMeta(pluginName, repoRoot);
  if (!meta?.openneko?.skill) return null;
  const pkgRoot = path.join(repoRoot, "node_modules", pluginName);
  const skillSrc = path.resolve(pkgRoot, meta.openneko.skill);
  if (!existsSync(skillSrc)) {
    // Package declared a skill but the folder isn't on disk — silently
    // skip. Don't fail the install over a malformed plugin package.
    return null;
  }
  const skillMd = path.join(skillSrc, "SKILL.md");
  let skillName = pluginName.split("/").pop() ?? pluginName;
  if (existsSync(skillMd)) {
    try {
      const content = await readFile(skillMd, "utf8");
      const frontmatterName = extractNameFromFrontmatter(content);
      if (frontmatterName) skillName = frontmatterName;
    } catch {
      // Treat as "use the package basename" — better than failing.
    }
  }
  const dest = path.join(
    skillsInstallDir ?? path.join(homedir(), ".openneko", "skills"),
    skillName,
  );
  await mkdir(path.dirname(dest), { recursive: true });
  await cp(skillSrc, dest, { recursive: true, force: true });
  return dest;
}

/**
 * Minimal SKILL.md name-field extractor. Reuses-by-duplication a tiny
 * slice of the frontmatter parser to avoid pulling @neko/llm into
 * plugin-install (cyclic dep risk + plugin-install ships independently).
 */
function extractNameFromFrontmatter(content: string): string | null {
  const lines = content.split(/\r?\n/);
  if (lines[0]?.trim() !== "---") return null;
  for (let i = 1; i < lines.length; i++) {
    const line = lines[i] ?? "";
    if (line.trim() === "---") return null;
    const m = /^name\s*:\s*(.+?)\s*$/.exec(line);
    if (m) {
      const raw = (m[1] ?? "").trim();
      const stripped = raw.replace(/^['"]|['"]$/g, "");
      return stripped || null;
    }
  }
  return null;
}
