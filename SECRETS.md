# Secrets

OpenNeko holds deployment secrets (channel and sign-in credentials, pack
connector keys, the model-gateway key, the Web Push VAPID key pair) and
per-operator OAuth credentials. OpenNeko encrypts all of them at rest with
the local `enc:v1` cipher (AES-256-GCM, keyed from
`~/.config/openneko/secret-key`).

## Where secrets live

OpenNeko keeps every secret in one place: the local secrets file
`~/.config/openneko/secrets.json`. Use `openneko secrets list`, `set` and
`unset` to manage the deployment secrets.

An older release could read deployment secrets from an Infisical vault. This
release removes that backend. When `config.json` still sets
`secrets.backend` to `"infisical"`, `openneko web` and the `openneko` CLI do not
start and print one message. To upgrade, copy each secret into the local file
with `openneko secrets set <section> <key>`, remove the `secrets` block from
`config.json`, then start again.

## Model keys

Admin, Settings, Models stores each model key in the database, encrypted
with the same cipher. `NEKO_EMBEDDING_API_KEY` is in the environment of
`openneko web`. Only `openneko web` reads these keys. It keeps them in its
key door and gives `openneko serve` a token for one call in place of a key.
The door puts the key on the call to the provider and replaces the key with
`[redacted]` in an error text ([AGENT_RUNTIME.md](AGENT_RUNTIME.md#the-key-door)).

## Secrets in run events

`openneko web` replaces each secret value with `[REDACTED]` before it stores
a run event or sends it to the live stream. This covers chat, channel, cron,
watcher, manual and API runs. The rule takes every value of 8 or more
characters in the secrets file, the operator OAuth tokens included, and
reads the file again when it changes. The rule catches a value that appears
as written. It does not catch an encoded or partial value.
