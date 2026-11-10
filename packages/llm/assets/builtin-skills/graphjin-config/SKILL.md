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
   mcp and serv. Every secret shows as `[REDACTED]`. When more than one data
   source is enabled, pass the `dataSourceId` that the admin chose.
3. For the meaning of a key, read `references/config-reference.md` beside
   this file. Read it only when you need it.
4. For a question, answer from the results of steps 1 and 2. Name the source,
   the role or the key that the answer depends on.
5. For a database source registration, call
   `mcp_neko_source_config_manager_list_source_secret_names` and use a stored
   name as `secretRef`. When the required name is absent, tell the admin to
   add it in Admin > Settings > GraphJin Config.
6. For an API source, use the managed OpenAPI asset from the submitted card.
   When the admin gives a hosted HTTPS URL in chat, call
   `mcp_neko_source_config_manager_import_openapi_spec`. When the admin
   uploaded a file, read the asset ID from the submitted values. When the
   card gives metadata only, call
   `mcp_neko_source_config_manager_list_openapi_specs` to find the asset ID.
7. For a local file source, use the `localFiles` manifest from the submitted
   card. For S3 or GCS, collect the bucket and the applicable prefix, region,
   endpoint, public base URL and presign TTL. GraphJin uses the deployment
   runtime identity for object-store credentials.
8. For a supported edit, call
   `mcp_neko_source_config_manager_request_source_config_change` with the
   confirmed fields. Before the call, compare the change with the current
   config from step 2. Tell the admin what the change does and what stays the
   same.
9. Summarize the proposal and its approval status. Say that nothing applies
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
  `request_source_config_change`.
- Database sources stay read-only. The host rejects a proposal that opens a
  write path on one.
- Treat `[REDACTED]` as "set, value hidden". Do not guess the value.

## Supported Edits

- `register_source`: give `name`, one customer source kind (`database`,
  `api` or `file`), and the fields for that kind. A database uses connection
  metadata and a stored `secretRef`. An API uses a managed OpenAPI
  `specAssetId`. A local file source uses the card's managed `localFiles`
  manifest. S3 and GCS use an object-store bucket and the deployment runtime
  identity. File sources get authenticated read-only access. A suitable
  database default is `read: authenticated`, `write: blocked` and
  `delete: blocked`.
- `set_source_access`: give the source name and the confirmed `read`,
  `write` and `delete` access modes.
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
