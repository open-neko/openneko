---
type: Skill
name: graphjin-config
description: "Use when viewing, explaining, or proposing changes to GraphJin sources, roles, access, security, runtime settings, and reload behavior."
---

# GraphJin Config

Use the source-config manager tools for GraphJin configuration requests. You
plan every change yourself. An admin approves each change before it applies.

## Workflow

1. Call `mcp_neko_source_config_manager_describe_source_graph`. It names the
   selected GraphJin data engine and lists its databases, capabilities and
   namespaces.
2. Call `mcp_neko_source_config_manager_describe_source_config`. It returns
   the current config: sources, roles, databases, tables, blocklist, system,
   mcp and serv. Every secret shows as `[REDACTED]`. Its `summary` gives
   plain sentences for each source and role. When more than one data source
   is enabled, pass the `dataSourceId` that the admin chose.
3. For the meaning of a key, read `references/config-reference.md` beside
   this file. Read it only when you need it.
4. For a question, answer from the results of steps 1 and 2. Use the
   sentences in `summary` when the admin asks what the config does. Name the
   source, the role or the key that the answer depends on.
5. First ask which system the data lives in. When a pack covers it, call
   `mcp_neko_source_config_manager_present_pack_setup` with the pack ID and
   skip steps 6 to 11. The pack carries a ready config, so the admin enters
   only the address and the login. The built-in packs with a ready config:
   `magento` (Magento Open Source & Adobe Commerce, its MySQL database),
   `shopify`, `google-workspace`, `slack`, `resend` and `parallel-search`.
   An admin can also name a pack that they uploaded.
   When the admin applies the card, a message says so. Then call
   `mcp_neko_source_config_manager_describe_source_graph` to confirm the new
   source, run one small read with the data tools, and say what the admin
   can ask next.
6. For a database source with no pack, ask plain questions, one at a time:
   which system (PostgreSQL, MySQL or MariaDB), where it runs (host and
   port), which database, which read-only login, which tables no one may
   see, and which tables only admins may see. Then call
   `mcp_neko_source_config_manager_present_source_form` with the answers. The
   admin types the password into the card. The card stores it and gives you
   its name as `secretRef`. To reuse a stored password, call
   `mcp_neko_source_config_manager_list_source_secret_names` and prefill that
   name. OpenNeko does not connect with a login that has no password. When
   the admin has no password for the login, ask them to give it one first.
7. For an API source, use the managed OpenAPI asset from the submitted card.
   When the admin gives a hosted HTTPS URL in chat, call
   `mcp_neko_source_config_manager_import_openapi_spec`. When the admin
   uploaded a file, read the asset ID from the submitted values. When the
   card gives metadata only, call
   `mcp_neko_source_config_manager_list_openapi_specs` to find the asset ID.
8. For a local file source, use the `localFiles` manifest from the submitted
   card. For S3 or GCS, collect the bucket and the applicable prefix, region,
   endpoint, public base URL and presign TTL. GraphJin uses the deployment
   runtime identity for object-store credentials.
9. For a supported edit, call
   `mcp_neko_source_config_manager_request_source_config_change` with the
   confirmed fields. Before the call, compare the change with the current
   config from step 2. Tell the admin what the change does and what stays the
   same.
10. OpenNeko checks every proposal before the admin sees "approve": first
   the answers, then GraphJin's live preview, then the config file with the
   pinned GraphJin loader. When the result has `problems`, the change is not
   filed. Each problem names the answer at fault in `field` and the fix in
   `message`. For a new source, the form comes back with each message beside
   its field. Tell the admin what to fix in one or two sentences and wait for
   the new answers. Do not repeat GraphJin's `detail` text unless the admin
   asks for it.
11. Summarize the proposal and its approval status. Say that nothing applies
   until an admin approves it.

A view or an explanation succeeds when the answer rests on the redacted config
from step 2. An edit succeeds when the response includes the proposal result
from `request_source_config_change`.

When a tool returns an error, report the error and the workflow step that needs
attention.

## Rules

- Never ask for a password, key or token in chat. Credentials go into the
  secure form, and you refer to them by stored name only.
- Never write YAML for the admin to paste. Every change goes through
  `request_source_config_change` or a pack's setup card.
- Database sources stay read-only. The host rejects a proposal that opens a
  write path on one.
- Treat `[REDACTED]` as "set, value hidden". Do not guess the value.

## Supported Edits

- `register_source`: give `name`, one customer source kind (`database`,
  `api` or `file`), and the fields for that kind. A database uses connection
  metadata, a `type` (`postgres`, `mysql` or `mariadb`), a stored
  `secretRef`, and optional `blockedTables` and `adminTables` lists. An API uses a managed OpenAPI
  `specAssetId`. A local file source uses the card's managed `localFiles`
  manifest. S3 and GCS use an object-store bucket and the deployment runtime
  identity. File sources get authenticated read-only access. A suitable
  database default is `read: authenticated`, `write: blocked` and
  `delete: blocked`.
- `set_source_access`: give the source name and the confirmed `read`,
  `write` and `delete` access modes.
- `set_source_tables`: give the database source name and one or more lists.
  `blockedTables`: no one may read them. `adminTables`: only admins may read
  them. `openTables`: they go back to the source's read mode. Use the table
  names from `describe_source_graph`.
- `add_role`: give the role name and the JWT match expression that the admin
  confirmed.
- `enable_api_writes`: give the API source, the spec key, the operation ID and
  `allowedRoles`. One proposal turns on API writes and exposes one operation
  as a GraphQL mutation for those roles.
- `set_source_capabilities` and `expose_api_operation`: change one write
  setting on an API source that already has writes on.

Use an unused source name for a registration. An access change patches the
named source. A role change replaces the role with the same name. A full
config replacement is a different operation. It needs the current config and
the admin's clear intent.
