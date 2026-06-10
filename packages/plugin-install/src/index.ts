export {
  allSecretValues,
  allSecretValuesFull,
  defaultSecretsPath,
  getOperatorCredential,
  isValidEnvKey,
  isValidOperatorId,
  listConnectedPluginsForOperator,
  listKeysForPlugin,
  listOperatorsForPlugin,
  OPERATORS_KEY,
  readFullSecretsFile,
  readFullSecretsFileSoft,
  readSecretsStore,
  readSecretsStoreSoft,
  setOperatorCredential,
  setSecret,
  unsetOperatorCredential,
  unsetSecret,
  writeFullSecretsFile,
  writeSecretsStore,
  type ConnectorCredential,
  type FullSecretsFile,
  type SecretsStore,
} from "./secrets-store.js";

export {
  FileSecretsResolver,
  type SecretsResolver,
} from "./secrets-resolver.js";

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
