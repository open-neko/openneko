# Secrets

OpenNeko holds deployment secrets (channel and sign-in credentials, pack
connector keys, the model-gateway key) and
per-operator OAuth credentials. OpenNeko encrypts all of them at rest with
the local `enc:v1` cipher (AES-256-GCM, keyed from
`~/.config/openneko/secret-key`).

## Where secrets live

OpenNeko keeps every secret in one place: the local secrets file
`~/.config/openneko/secrets.json`. Use `openneko secrets list`, `set` and
`unset` to manage the deployment secrets.

An older release could read deployment secrets from an Infisical vault. This
release removes that backend. When `config.json` still sets
`secrets.backend` to `"infisical"`, the worker and `openneko web` do not
start and print one message. To upgrade, copy each secret into the local file
with `openneko secrets set <section> <key>`, remove the `secrets` block from
`config.json`, then start again.
