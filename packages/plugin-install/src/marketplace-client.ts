// Federated-marketplace client. A "marketplace" is a JSON document at
// a stable URL listing plugins. The official marketplace at
// https://open-neko.github.io/plugins/marketplace.json is OpenNeko's
// own; anyone can host their own at any URL and operators trust them
// via the openneko CLI's `marketplace add` command.
//
// The shape mirrors plugins/schema/marketplace.schema.json. We don't
// depend on Ajv at this layer — Ajv validation runs at publish time
// in the marketplace repo. Here we only need structural sanity.

export const OFFICIAL_MARKETPLACE_NAME = "official";
export const OFFICIAL_MARKETPLACE_URL =
  "https://open-neko.github.io/plugins/marketplace.json";

export interface MarketplaceEnvRequirement {
  key: string;
  required?: boolean;
  secret?: boolean;
  description: string;
}

export interface MarketplacePermissions {
  network: string[];
  env: MarketplaceEnvRequirement[];
}

export interface MarketplaceActionDeclaration {
  kind: string;
  description: string;
}

export interface MarketplaceCapabilities {
  action?: { kinds: MarketplaceActionDeclaration[] };
  auth?: { providerLabel?: string };
}

export interface MarketplaceVersion {
  version: string;
  integrity: string;
  permissions: MarketplacePermissions;
  capabilities: MarketplaceCapabilities;
  publishedAt: string;
  yanked?: boolean;
  yanked_reason?: string;
}

export interface MarketplacePlugin {
  name: string;
  title: string;
  description: string;
  source: string;
  homepage?: string;
  maintainers?: Array<{ name: string; url?: string }>;
  versions: MarketplaceVersion[];
}

export interface Marketplace {
  name: string;
  owner: string;
  description: string;
  homepage?: string;
  plugins: MarketplacePlugin[];
}

export interface MarketplaceClient {
  fetch(url: string): Promise<Marketplace>;
}

export function createMarketplaceClient(options: {
  fetchImpl?: typeof fetch;
} = {}): MarketplaceClient {
  const fetchImpl = options.fetchImpl ?? fetch;
  return {
    async fetch(url: string): Promise<Marketplace> {
      const res = await fetchImpl(url);
      if (!res.ok) {
        throw new Error(
          `marketplace: ${url} returned ${res.status} ${res.statusText}`,
        );
      }
      const parsed = (await res.json()) as unknown;
      if (!isMarketplace(parsed)) {
        throw new Error(`marketplace: ${url} did not match the expected shape`);
      }
      return parsed;
    },
  };
}

export function findPlugin(
  marketplace: Marketplace,
  pluginName: string,
): MarketplacePlugin | null {
  return marketplace.plugins.find((p) => p.name === pluginName) ?? null;
}

export function pickInstallVersion(
  plugin: MarketplacePlugin,
  requested?: string,
): MarketplaceVersion {
  const live = plugin.versions.filter((v) => !v.yanked);
  if (live.length === 0) {
    throw new Error(
      `marketplace: every published version of ${plugin.name} is yanked`,
    );
  }
  if (requested) {
    const match = live.find((v) => v.version === requested);
    if (!match) {
      throw new Error(
        `marketplace: ${plugin.name} has no published non-yanked version ${requested}`,
      );
    }
    return match;
  }
  return live.reduce((latest, v) =>
    semverCompare(v.version, latest.version) > 0 ? v : latest,
  );
}

function isMarketplace(x: unknown): x is Marketplace {
  if (typeof x !== "object" || x === null) return false;
  const o = x as Record<string, unknown>;
  return (
    typeof o.name === "string" &&
    typeof o.owner === "string" &&
    typeof o.description === "string" &&
    Array.isArray(o.plugins)
  );
}

export function semverCompare(a: string, b: string): number {
  const pa = parseSemver(a);
  const pb = parseSemver(b);
  if (pa.major !== pb.major) return pa.major - pb.major;
  if (pa.minor !== pb.minor) return pa.minor - pb.minor;
  if (pa.patch !== pb.patch) return pa.patch - pb.patch;
  if (!pa.pre && pb.pre) return 1;
  if (pa.pre && !pb.pre) return -1;
  if (pa.pre && pb.pre) return pa.pre.localeCompare(pb.pre);
  return 0;
}

function parseSemver(v: string): {
  major: number;
  minor: number;
  patch: number;
  pre: string | null;
} {
  const m = /^(\d+)\.(\d+)\.(\d+)(?:-(.+))?$/.exec(v);
  if (!m) throw new Error(`invalid semver: ${v}`);
  return {
    major: Number(m[1]),
    minor: Number(m[2]),
    patch: Number(m[3]),
    pre: m[4] ?? null,
  };
}
