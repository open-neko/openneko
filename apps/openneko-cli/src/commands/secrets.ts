import {
  isValidEnvKey,
  listKeysForPlugin,
  readSecretsStore,
  setSecret,
  unsetSecret,
  writeSecretsStore,
} from "@open-neko/plugin-install";

export interface SecretsCommonOptions {
  /** Override the config dir (tests use a tmp dir). */
  configDir?: string;
}

export interface SecretsSetOptions extends SecretsCommonOptions {
  plugin: string;
  key: string;
  /** If omitted, the CLI prompts the operator (TTY only). */
  value?: string;
}

export interface SecretsSetResult {
  plugin: string;
  key: string;
  newKey: boolean;
}

export async function runSecretsSet(
  options: SecretsSetOptions,
): Promise<SecretsSetResult> {
  if (!isValidEnvKey(options.key)) {
    throw new Error(
      `secrets set: key "${options.key}" must be UPPER_SNAKE_CASE`,
    );
  }
  if (options.value === undefined) {
    throw new Error("secrets set: value required (pass via argv or stdin)");
  }
  const store = await readSecretsStore(options.configDir);
  const existingKeys = new Set(listKeysForPlugin(store, options.plugin));
  const next = setSecret(store, options.plugin, options.key, options.value);
  await writeSecretsStore(next, options.configDir);
  return {
    plugin: options.plugin,
    key: options.key,
    newKey: !existingKeys.has(options.key),
  };
}

export interface SecretsUnsetOptions extends SecretsCommonOptions {
  plugin: string;
  key: string;
}

export interface SecretsUnsetResult {
  plugin: string;
  key: string;
  removed: boolean;
}

export async function runSecretsUnset(
  options: SecretsUnsetOptions,
): Promise<SecretsUnsetResult> {
  const store = await readSecretsStore(options.configDir);
  const { store: next, removed } = unsetSecret(
    store,
    options.plugin,
    options.key,
  );
  if (removed) await writeSecretsStore(next, options.configDir);
  return { plugin: options.plugin, key: options.key, removed };
}

export interface SecretsListOptions extends SecretsCommonOptions {
  /** When omitted, list every plugin with its keys. */
  plugin?: string;
}

export interface SecretsListEntry {
  plugin: string;
  keys: string[];
}

export interface SecretsListResult {
  entries: SecretsListEntry[];
}

export async function runSecretsList(
  options: SecretsListOptions,
): Promise<SecretsListResult> {
  const store = await readSecretsStore(options.configDir);
  if (options.plugin) {
    return {
      entries: [
        { plugin: options.plugin, keys: listKeysForPlugin(store, options.plugin) },
      ],
    };
  }
  return {
    entries: Object.keys(store)
      .sort()
      .map((pkg) => ({ plugin: pkg, keys: listKeysForPlugin(store, pkg) })),
  };
}
