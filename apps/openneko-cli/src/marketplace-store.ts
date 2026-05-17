// Local store for the operator's trusted marketplaces. Lives at
// $XDG_CONFIG_HOME/openneko/marketplaces.json (default
// ~/.config/openneko/marketplaces.json). The official marketplace is
// auto-added on first read so every operator gets it without having to
// run `openneko marketplace add` themselves.
import { existsSync } from "node:fs";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import {
  OFFICIAL_MARKETPLACE_NAME,
  OFFICIAL_MARKETPLACE_URL,
} from "@open-neko/plugin-install";

export interface TrustedMarketplace {
  /** Slugified marketplace.json's `name`. Unique within this store. */
  name: string;
  url: string;
  /** ISO date the operator added this marketplace. */
  addedAt: string;
  /** True for the OpenNeko-shipped official marketplace. */
  official?: boolean;
}

export interface MarketplaceStore {
  marketplaces: TrustedMarketplace[];
}

const STORE_FILENAME = "marketplaces.json";

function configDir(): string {
  const xdg = process.env.XDG_CONFIG_HOME;
  if (xdg) return path.join(xdg, "openneko");
  return path.join(process.env.HOME ?? "/tmp", ".config", "openneko");
}

function storePath(overrideDir?: string): string {
  return path.join(overrideDir ?? configDir(), STORE_FILENAME);
}

const OFFICIAL: TrustedMarketplace = {
  name: OFFICIAL_MARKETPLACE_NAME,
  url: OFFICIAL_MARKETPLACE_URL,
  addedAt: "1970-01-01",
  official: true,
};

function defaultStore(): MarketplaceStore {
  return { marketplaces: [OFFICIAL] };
}

export async function readStore(overrideDir?: string): Promise<MarketplaceStore> {
  const file = storePath(overrideDir);
  if (!existsSync(file)) {
    await writeStore(defaultStore(), overrideDir);
    return defaultStore();
  }
  const raw = await readFile(file, "utf8");
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch (err) {
    throw new Error(
      `marketplaces config at ${file} is invalid JSON: ${err instanceof Error ? err.message : err}`,
    );
  }
  if (!isStore(parsed)) {
    throw new Error(`marketplaces config at ${file} has unexpected shape`);
  }
  // Make sure the official marketplace is always present — operators
  // can't accidentally orphan themselves from the canonical source.
  const hasOfficial = parsed.marketplaces.some(
    (m) => m.name === OFFICIAL_MARKETPLACE_NAME,
  );
  if (!hasOfficial) {
    const next: MarketplaceStore = {
      marketplaces: [OFFICIAL, ...parsed.marketplaces],
    };
    await writeStore(next, overrideDir);
    return next;
  }
  return parsed;
}

export async function writeStore(
  store: MarketplaceStore,
  overrideDir?: string,
): Promise<void> {
  const file = storePath(overrideDir);
  await mkdir(path.dirname(file), { recursive: true });
  await writeFile(file, JSON.stringify(store, null, 2) + "\n", "utf8");
}

export function slugify(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/(^-|-$)/g, "")
    .slice(0, 40);
}

export function addToStore(
  store: MarketplaceStore,
  entry: TrustedMarketplace,
): MarketplaceStore {
  if (store.marketplaces.some((m) => m.name === entry.name)) {
    throw new Error(
      `marketplace "${entry.name}" already trusted — remove it first if you want to change its URL`,
    );
  }
  if (store.marketplaces.some((m) => m.url === entry.url)) {
    throw new Error(`marketplace URL ${entry.url} already trusted`);
  }
  return { marketplaces: [...store.marketplaces, entry] };
}

export function removeFromStore(
  store: MarketplaceStore,
  nameOrUrl: string,
): { store: MarketplaceStore; removed: TrustedMarketplace | null } {
  const target = store.marketplaces.find(
    (m) => m.name === nameOrUrl || m.url === nameOrUrl,
  );
  if (!target) return { store, removed: null };
  if (target.official) {
    throw new Error(
      `marketplace "${target.name}" is the official OpenNeko marketplace — refusing to remove`,
    );
  }
  return {
    store: {
      marketplaces: store.marketplaces.filter((m) => m !== target),
    },
    removed: target,
  };
}

function isStore(x: unknown): x is MarketplaceStore {
  if (typeof x !== "object" || x === null) return false;
  const o = x as { marketplaces?: unknown };
  if (!Array.isArray(o.marketplaces)) return false;
  return o.marketplaces.every(
    (m) =>
      typeof m === "object" &&
      m !== null &&
      typeof (m as { name?: unknown }).name === "string" &&
      typeof (m as { url?: unknown }).url === "string",
  );
}
