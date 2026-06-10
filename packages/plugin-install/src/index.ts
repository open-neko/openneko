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
} from "./secrets-store";

export {
  FileSecretsResolver,
  type SecretsResolver,
} from "./secrets-resolver";

export {
  InfisicalSecretsResolver,
  folderToPluginName,
  pluginNameToFolder,
  type InfisicalConfig,
} from "./infisical-resolver";

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
} from "./marketplace-client";

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
} from "./manifest";

export {
  parseInstallSpec,
  runInstall,
  type InstallOptions,
  type InstallResult,
  type ParsedSpec,
  type TrustedMarketplace,
} from "./run-install";
