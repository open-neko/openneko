# GraphJin config reference

This reference describes GraphJin v3.21.18.

Use it to read the `gj_config(id: "current")` row and to explain a change. It
summarises the GraphJin `core` and `serv` modules (Apache 2.0).

## The gj_config row

The row has these top-level fields.

| Field | Meaning |
|-------|---------|
| `sources_used` | `true` when the config uses `sources`. Without it, GraphJin runs in legacy database-only mode. |
| `sources` | The declared providers: databases, APIs, files, code. |
| `databases` | The runtime database map. GraphJin builds it from `kind: database` and `kind: code` sources. |
| `tables` | Table overrides: aliases, columns, relationships, `read_only`, `order_by`. |
| `roles` | Named roles with `match` rules and table rules. |
| `blocklist` | Tables and columns that no query can use. |
| `system` | Capabilities and root access for the built-in `gj_*` roots. |
| `workflows`, `relationships`, `functions`, `resolvers` | Workflow settings, extra graph edges, function return types, custom resolvers. |
| `mcp` | MCP server switches. |
| `serv` | Server settings: log level, rate limiter, auth, caching, reload. |
| `catalog_revision` | The current catalog revision. A config write must send it back. |
| `config_path`, `active_database`, `updated_at` | Context only. |

Some nested objects have no JSON tags, so their keys show in Go field case.
Examples: role tables (`Name`, `Query`, `Insert`, `Block`), table entries
(`Name`, `Columns`), and `serv.auth`, `serv.rate_limiter`, `serv.caching`.
Duration fields show as integer nanoseconds.

The row does not show `identity`, `default_block`, `disable_allow_list`, or
`mode`. Say so when a question depends on them.

## Sources

Each entry in `sources` declares one provider. `kind` is required.

| Kind | Meaning |
|------|---------|
| `database` | A SQL database. Default `type` is `postgres`. |
| `api` | A remote HTTP API described by OpenAPI 3 specs. |
| `file` | An object store or local directory, exposed as a virtual table. |
| `code` | A code index (CodeSQL). OpenNeko does not register this kind. |

GraphJin rejects old kind names (`sql`, `openapi`, `filesystem`, `codesql`).

OpenNeko keeps every database source read-only, and a proposal that opens a
write path on a database source is rejected.

### Common fields

| Key | Type | Meaning |
|-----|------|---------|
| `name` | string | Unique source name. Other keys refer to it. |
| `default` | bool | Marks the default source. |
| `read_only` | bool | Blocks every write through this source. See below. |
| `capabilities` | map of bool | Per-surface switches. See the capability table. |
| `access` | object | Access modes and table lists. See "Access". |

### Database fields

| Key | Type | Meaning |
|-----|------|---------|
| `type` | string | `postgres`, `mysql`, `mariadb`, `sqlite`, `oracle`, `mssql`, `snowflake`, and others. |
| `host`, `port`, `dbname`, `user`, `schema` | string, int | Connection parts. `path` is the SQLite file. |
| `connection_string`, `password` | string | Credentials. Always redacted. |
| `pool_size`, `max_connections`, `max_open_conns`, `max_idle_conns` | int | Pool limits. |
| `max_connection_idle_time`, `max_connection_life_time`, `ping_timeout` | duration | Connection lifetimes and health check timeout. |
| `enable_tls`, `server_name`, `server_cert`, `client_cert`, `client_key` | mixed | TLS settings. `client_key` is redacted. |
| `encrypt`, `trust_server_certificate` | bool | MSSQL TLS options. |
| `private_key_path`, `private_key_pem`, `key_passphrase` | string | Snowflake key-pair auth. All redacted. |

### API fields

| Key | Type | Meaning |
|-----|------|---------|
| `specs_dir` | string | Directory of OpenAPI files. Only one value is allowed across API sources. |
| `specs` | map | Per-spec settings, keyed by spec file name without extension. |
| `specs.<spec>.base_url` | string | Overrides the server URL in the spec. |
| `specs.<spec>.auth` | object | Outbound auth. `scheme` is `bearer`, `basic`, `api_key`, `oauth2_client_credentials` or `token_exchange`. Token fields are redacted. |
| `specs.<spec>.joins` | map | Joins an operation to a database table column. |
| `specs.<spec>.concurrency`, `timeout`, `max_request_bytes`, `max_response_bytes` | mixed | Limits on upstream calls. |
| `specs.<spec>.document` | string | The OpenAPI document inline. It wins over a file of the same key. |
| `specs.<spec>.operations.<operationId>` | object | Per-operation overrides, listed below. |

Operation override keys:

| Key | Type | Meaning |
|-----|------|---------|
| `expose_as` | string | GraphQL field name for the operation. |
| `expose_mutation` | bool | Explicit opt-in for a non-GET operation. A spec alone never exposes a write. |
| `allowed_roles` | list | Roles that may call a non-GET operation. It adds to capability and access checks. |
| `disabled`, `expose_top_level`, `result_path`, `defaults` | mixed | Hide the operation, expose it at top level, pick the result path, set parameter defaults. |
| `retry_on_auth_failure` | bool | Allows one retry after a 401. |

GraphJin checks an API call in this order: HTTP method, source `read_only`
(non-GET only), the capability, the access mode, then `allowed_roles` (non-GET
only). The first failed check stops the call.

### File fields

| Key | Type | Meaning |
|-----|------|---------|
| `backend` | string | `local`, `s3` or `gcs`. |
| `root` | string | Local directory. Local backend only. |
| `bucket`, `prefix`, `region`, `endpoint` | string | Bucket, key prefix, and S3 region and endpoint override. |
| `presign_ttl`, `public_base_url` | duration, string | Presigned URL lifetime, and a CDN host that replaces the presigned host. |
| `max_list_page_size` | int | Cap on entries per list call. |

### read_only

- Database: blocks all mutations and DDL on that database.
- API: blocks every non-GET operation, before any other check.
- File: disables managed writes and watch invalidation.
- GraphJin records which sources and databases were read-only at startup. A
  later config change cannot set `read_only: false` on them. GraphJin keeps
  `true` and reports "read_only preserved as true (tamper-protected)".

### Capabilities

Keys depend on the kind. Defaults depend on the deployment mode. Code sources
also have `code.*` keys.

| Kind | Key | Meaning | dev / prod / agentic default |
|------|-----|---------|------------------------------|
| database | `data.read` | Read data. | on / on / on |
| database | `data.write` | Write data. `read_only` blocks it. | on / on / on |
| database | `schema.read` | Read schema metadata. | on / on / on |
| database | `schema.write` | Change the schema. `read_only` blocks it. | on / off / off |
| file | `files.list`, `files.read` | List and read objects. | on / on / on |
| file | `files.write`, `files.delete`, `files.watch` | Write, delete, watch objects. | on / off / off |
| api | `api.read` | Call GET operations. | on / on / on |
| api | `api.write` | Call POST, PUT and PATCH operations. | on / off / off |
| api | `api.delete` | Call DELETE operations. | off / off / off |

## Roles

`roles` is a list. Each role has these keys.

| Key | Type | Meaning |
|-----|------|---------|
| `name` | string | Role name. `anon` and `user` are built in. Names that start with `__` are reserved. |
| `comment` | string | Free text. |
| `match` | string | A boolean rule that selects this role. |
| `tables` | list | Per-table rules: `Query`, `Insert`, `Update`, `Upsert`, `Delete`, each with `Filters`, `Columns`, `Presets` (insert, update, upsert), `Block`. `Query` also has `Limit`. |

How GraphJin picks a role:

- A caller without a user ID gets `anon`. A known caller gets `user` when no
  other role applies. In source mode, GraphJin reads role names from the JWT
  claims in `identity.role_claims` (default `role`, `roles`).
- `match` works with the identity query (`identity.query`, alias
  `roles_query`). That query must use `$user_id`. GraphJin runs it and tests
  each `match` against the returned row.
- With a SQL identity query, `match` is a SQL condition, for example
  `role = 'support'`.
- With a GraphQL identity query, `match` uses a small grammar: field paths,
  string, number, boolean and `null` literals, `=`, `!=`, `<>`, `<`, `<=`,
  `>`, `>=`, `and`, `or`, `not`, `is null`, `is not null`, and parentheses.
- `identity.role_mode` is `first` (default) or `union`. `union` merges the
  table rules of every matching role.

In source mode, use source `access` for table permissions. GraphJin guidance
forbids `roles[].tables` filters, presets, columns and block rules there.

## Access

`sources[].access` sets who can read, write and delete. GraphJin compiles it
into per-role table rules for database sources.

### Modes

| Mode | Who passes | Row rule |
|------|-----------|----------|
| `public` | Everyone, including `anon`. Read only. | None. |
| `authenticated` | Every role except `anon`. | None. |
| `account` | Every role except `anon`. | Rows where `namespace_column` equals `$account_id`. Inserts preset that column. |
| `owner` | Every role except `anon`. | Rows where `owner_column` equals `$user_id`. Inserts preset that column. |
| `admin` | Only roles in `identity.admin_roles`. Default `admin`. | None. |
| `blocked` | Nobody. | Not applicable. |

`read` accepts all six modes. `write` and `delete` accept every mode except
`public`. Admin roles never get the account or owner row filter.

### Access keys

| Key | Type | Meaning |
|-----|------|---------|
| `read`, `write`, `delete` | string | Mode for reads, for inserts, updates and upserts, and for deletes. |
| `namespace_column`, `owner_column` | string | Tenant column for `account` mode, owner column for `owner` mode. |
| `missing_namespace_column` | string | `block` or `allow`. Controls a table without the namespace column. |
| `public_tables` | list | Read `public`, write and delete `blocked`. |
| `admin_tables` | list | Read `admin`, write and delete `blocked`. |
| `blocked_tables` | list | Hidden from every role. |
| `grants` | list | Per-role read grants. See below. |

GraphJin fills these defaults when a key is empty:

| Kind | read | write | delete | Other |
|------|------|-------|--------|-------|
| database | `account` | `blocked` | `blocked` | `namespace_column: account_id`, `owner_column: user_id`, `missing_namespace_column: block` |
| api | `authenticated` | `blocked` | `blocked` | None |

Table list rules:

- A list entry matches `table`, `schema.table`, `database:table` or
  `database:schema.table`. Case does not matter.
- `blocked_tables` wins over `public_tables`, which wins over `admin_tables`.
- A table without the namespace column becomes `blocked` (`block`) or
  `authenticated` (`allow`) in `account` mode. In `owner` mode, a table
  without the owner column becomes `blocked`.

A grant has `role` and `tables`. Each table has `name`, `columns` and an
optional `filter`. A grant replaces the read mode for that role and table. The
account or owner filter still applies. A grant on a blocked table fails.

## Security

| Key | Where | Meaning |
|-----|-------|---------|
| `default_block` | core | Blocks every table for `anon` unless the `anon` role opens it. Default `true`. Not in the row. |
| `disable_allow_list`, `disable_production_security` | core | Turn off the saved-query allow list, or all production protections. Not in the row. |
| `blocklist` | core | Table and column names that no query can use. |
| `tables[].read_only` | core | Blocks mutations on one table. |
| `system.root_access` | core | Access mode per `gj_*` root. Explicit entries win over mode defaults. |
| `system.capabilities` | core | Switches for built-in features. See below. |
| `serv.auth` | serv | Inbound auth. Shown with Go field names. |
| `serv.auth_fail_block` | serv | Returns HTTP 401 on auth failure. Not in the row. |

`serv.auth` keys: `Type` (`jwt` or `header`), `Development`, `Cookie`
(redacted), `Header` (`Name`, `Value`, `Exists`), and `JWT`. `JWT` has
`Provider` (`auth0`, `firebase`, `jwks`, `other`), `Secret` (redacted),
`PubKey`, `PubKeyType`, `Audience`, `Issuer`, `JWKSURL`, `JWKSRefresh` and
`JWKSMinRefresh`.

Identity keys (config file only, not in the row): `user_id_claim` (default
`sub`), `role_claims`, `namespace_claim` (default `account_id`),
`admin_roles`, `group_claims`, `query`, `role_mode`. `gj_config` updates do
not accept `identity`.

### System roots

`system.root_access` takes a mode per root. Defaults by deployment mode:

| Root | dev | agentic | prod |
|------|-----|---------|------|
| `gj_catalog` | public | public | admin |
| `gj_artifacts`, `gj_watch`, `gj_watch_event`, `gj_task`, `gj_task_entry`, `gj_workflow`, `gj_workflow_execution` | public | owner | admin |
| `gj_runtime`, `gj_security`, `gj_config` | public | admin | admin |

`system.capabilities` keys: `catalog.read`, `security.read`, `config.read`,
`config.write`, `runtime.read`, `raw_graphql.query`, `raw_graphql.mutate`,
`schema.reload`, `schema.write`, `dev_tools.read`, `legacy_discovery.read`.
All are on in dev. In prod only `catalog.read` is on. In agentic,
`catalog.read` and `runtime.read` are on.

### MCP switches

| Key | Meaning | Writable through gj_config |
|-----|---------|----------------------------|
| `disable` | Turns off the MCP server. | No |
| `only` | MCP-only mode. Turns off GraphQL, REST, web UI and OpenAPI endpoints. | No |
| `allow_mutations` | Raw GraphQL mutations through MCP. | No |
| `allow_raw_queries` | Arbitrary GraphQL queries, not only saved queries. | Yes |
| `allow_config_updates` | Config writes through `gj_config`. Without it, every config write fails. | Yes |
| `allow_schema_reload`, `allow_schema_updates` | Manual schema reload, and DDL preview and apply. | Yes |
| `allow_workflow_updates`, `allow_workflow_execution` | Workflow writes through `gj_workflow`, and legacy HTTP workflow execution. | Yes |
| `allow_dev_tools` | SQL explain, relationship and permission audit tools. | Yes |
| `legacy_discovery` | Legacy HTTP discovery endpoints. | Yes |
| `stdio_user_id`, `stdio_user_role`, `cursor_cache_ttl`, `cursor_cache_size` | Stdio identity and cursor cache limits. | No |

The `allow_*` switches default to `false`. Dev mode turns most of them on.

## Runtime settings

These keys appear under `serv` in the row.

| Key | Type | Meaning | Reload |
|-----|------|---------|--------|
| `production` | bool | Legacy production switch. It turns off file-watch reload and saving config to disk. | Not writable |
| `log_level`, `log_format` | string | `debug`, `info`, `warn`, `error`. Format `auto`, `json`, `simple`. | restart |
| `web_ui` | bool | Web UI. On in dev and agentic, off in prod. | restart |
| `http_compress`, `server_timing` | bool | HTTP compression and the Server-Timing header. Both default `true`. | restart |
| `rate_limiter` | object | `Rate` (events per second), `Bucket` (burst size), `IPHeader` (client IP header). | restart |
| `agent` | object | Server-side GraphJin agent settings. Some fields are writable. | hot |
| `reload_on_config_change` | bool | Restarts the process when a config file changes. | Not writable |
| `caching` | object | `Disable`, `TTL` (default 3600 s), `FreshTTL` (default 300 s), `ExcludeTables`. | Not writable |
| `redis`, `uploads`, `runtime_events` | object | Shared cache `URL`, multipart upload limits, runtime event retention. | Not writable |
| `cors_*`, `app_name`, `host`, `port`, `host_port` | mixed | CORS, app name and listen address. | Not writable |
| `secrets_keystore_configured` | bool | `true` when a keystore key is set. The key never leaves the server. | Not writable |

## Reload behaviour

A source-mode config write has three steps.

1. Read `catalog_revision` from `gj_config`.
2. Send the patch with `mode: "preview"` and `expected_catalog_revision`.
   GraphJin validates it, builds a staged runtime, and returns `valid`,
   `preview_id`, `expires_at`, `change_summary_json`, `findings_json` and
   `errors_json`. Nothing changes yet.
3. Send the exact same patch with `mode: "apply"`, the `preview_id`, and the
   same `expected_catalog_revision`.

A preview expires after 10 minutes. A changed payload or a stale catalog
revision makes the apply fail: read `gj_config` again and start over. Stop
when the preview returns `valid: false` or a high or critical finding for the
change. `mcp.config_update_timeout` (default 3600 seconds) bounds one update.

The response classifies the change:

| Field | Values | Meaning |
|-------|--------|---------|
| `scope` | `core`, `serv`, `mixed` | Which part of the config changed. An `mcp` change counts as `serv`. |
| `reload_mode` | `hot`, `restart` | `hot` takes effect now. `restart` takes effect after the next restart. |
| `reload_strategy` | `full`, `source_scoped` | For core changes only. `source_scoped` reloads only the changed sources. `full` rebuilds the whole runtime. |

Source, role, table, blocklist, system, `mcp` and `serv.agent` changes apply
hot. The `serv` keys marked "restart" above need a restart. `serv.auth`,
`serv.redis` and `serv.uploads` are read-only on `gj_config`: change them in
the config file, then restart. GraphJin uses `source_scoped` when only sources
changed and the change stays inside them. Otherwise it uses `full`.

Outside production, GraphJin saves an applied change to the config file. In
production, the change stays in memory, and a restart loses it.

`reload_on_config_change` watches the config directory for `.yml`, `.yaml`,
`.json` and `.toml` writes. GraphJin validates the new file, then restarts the
process. It does nothing in production.

## Redaction

GraphJin replaces a sensitive value with the string `"[REDACTED]"`. It decides
by key name, not by value. A key is sensitive when its lower-case name, with
`-` changed to `_`, contains `password`, `secret`, `token`, `passphrase`,
`private_key`, `client_key`, `api_key`, `apikey`, `authorization` or `cookie`.
The exact names `connection_string` and `key_value` are also sensitive.

Read the result this way:

- `"[REDACTED]"` means the key is present and set. The value is hidden. Do not
  guess it.
- When a sensitive key holds an object, the whole object shows as
  `"[REDACTED]"`, for example `token_from_request`.
- GraphJin does not check values. A credential inside a non-sensitive key,
  such as a URL, is not redacted by this rule.
- `redacted_paths` is an empty list in this version. Do not rely on it.

## OpenNeko actions and the keys they touch

OpenNeko sends each action to `gj_config` as an additive patch.

| Action | Keys it sets |
|--------|--------------|
| `add_role` | `roles[]`: `name`, `match`, optional `comment`. |
| `set_source_access` | `source_patches[]`: `name` and `access.read`, `access.write`, `access.delete`. |
| `set_source_capabilities` | `update_sources[]`: `name` and `capabilities["api.write"]`, `capabilities["api.delete"]`. |
| `expose_api_operation` | `update_sources[]`: `specs.<spec>.operations.<operationId>` with `expose_mutation`, `allowed_roles`, optional `expose_as`. |
| `enable_api_writes` | `update_sources[]`: `read_only: false`, `capabilities["api.write"]` (and `api.delete` when asked), the operation override above. `source_patches[]`: `access.write` (`authenticated` or `admin`). |
| `register_source` | `update_sources[]`: one new source with `name` and `kind`, and `access` (`read`, `write`, `delete`). Database: `read_only: true`, `type`, `host`, `port`, `dbname`, `user`, `password` from a stored secret. API: `specs_dir`. File: `backend`, `root` or `bucket`, `prefix`, `region`, `endpoint`, `public_base_url`, `presign_ttl`, `read_only: true`, `max_list_page_size: 500`, and `files.*` capabilities with only list and read on. |

`update_sources` merges by name. Omitted fields stay. `null` clears a field.
Arrays replace. Nested objects merge. `source_patches` changes only the access
fields that it names.
