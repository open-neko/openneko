// The enc:v1 cipher moved to @neko/secret-crypt (SEC1) so the
// plugin-install secrets store and @neko/db local-config can share it
// without depending on @neko/llm. Re-exported here for existing callers.
export {
  maybeEncryptSecret,
  maybeDecryptSecret,
  _resetSecretKeyCacheForTesting,
} from "@neko/secret-crypt";
