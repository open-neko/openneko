import {
  createMarketplaceClient,
  type MarketplaceClient,
} from "@open-neko/plugin-install";
import {
  addToStore,
  readStore,
  removeFromStore,
  slugify,
  writeStore,
  type TrustedMarketplace,
} from "../marketplace-store.js";

export interface MarketplaceCommonOptions {
  /** Override the config dir (tests use a tmp dir). */
  configDir?: string;
  client?: MarketplaceClient;
}

export interface MarketplaceListResult {
  marketplaces: TrustedMarketplace[];
}

export async function runMarketplaceList(
  options: MarketplaceCommonOptions = {},
): Promise<MarketplaceListResult> {
  const store = await readStore(options.configDir);
  return { marketplaces: store.marketplaces };
}

export interface MarketplaceAddOptions extends MarketplaceCommonOptions {
  url: string;
}

export interface MarketplaceAddResult {
  added: TrustedMarketplace;
  marketplaceName: string;
  pluginCount: number;
}

/**
 * Fetches the marketplace at `url`, verifies it parses, derives a stable
 * local name from the marketplace.json's own `name` field, and adds it
 * to the trusted store.
 */
export async function runMarketplaceAdd(
  options: MarketplaceAddOptions,
): Promise<MarketplaceAddResult> {
  const client = options.client ?? createMarketplaceClient();
  const marketplace = await client.fetch(options.url);
  const slug = slugify(marketplace.name);
  if (!slug) {
    throw new Error(
      `marketplace at ${options.url} has an empty or unparseable name`,
    );
  }
  const entry: TrustedMarketplace = {
    name: slug,
    url: options.url,
    addedAt: new Date().toISOString().slice(0, 10),
  };
  const store = await readStore(options.configDir);
  const next = addToStore(store, entry);
  await writeStore(next, options.configDir);
  return {
    added: entry,
    marketplaceName: marketplace.name,
    pluginCount: marketplace.plugins.length,
  };
}

export interface MarketplaceRemoveOptions extends MarketplaceCommonOptions {
  nameOrUrl: string;
}

export interface MarketplaceRemoveResult {
  removed: TrustedMarketplace | null;
}

export async function runMarketplaceRemove(
  options: MarketplaceRemoveOptions,
): Promise<MarketplaceRemoveResult> {
  const store = await readStore(options.configDir);
  const { store: next, removed } = removeFromStore(store, options.nameOrUrl);
  if (removed) await writeStore(next, options.configDir);
  return { removed };
}
