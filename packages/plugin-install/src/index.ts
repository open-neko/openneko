export {
  allSecretValues,
  defaultSecretsPath,
  isValidEnvKey,
  listKeysForPlugin,
  readSecretsStore,
  readSecretsStoreSoft,
  setSecret,
  unsetSecret,
  writeSecretsStore,
  type SecretsStore,
} from "./secrets-store.js";

export {
  createMarketplaceClient,
  findPlugin,
  OFFICIAL_MARKETPLACE_NAME,
  OFFICIAL_MARKETPLACE_URL,
  pickInstallVersion,
  semverCompare,
  type Marketplace,
  type MarketplaceClient,
  type MarketplaceEnvRequirement,
  type MarketplacePlugin,
  type MarketplaceVersion,
} from "./marketplace-client.js";

export {
  emptyManifest,
  manifestPathFor,
  PLUGIN_MANIFEST_FILE,
  PLUGIN_MANIFEST_PATH_ENV,
  PLUGIN_MANIFEST_SCHEMA_URL,
  readManifest,
  removeEntry,
  upsertEntry,
  writeManifest,
  type Manifest,
  type ManifestEntry,
} from "./manifest.js";

export {
  parseInstallSpec,
  runInstall,
  type InstallOptions,
  type InstallResult,
  type ParsedSpec,
  type TrustedMarketplace,
} from "./run-install.js";
