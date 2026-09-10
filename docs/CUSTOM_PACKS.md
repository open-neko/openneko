# Install custom solution packs

For pack authors, see [PACKS.md](../PACKS.md) for layout, artifact contracts, validation, and the Git/PR workflow.

Custom packs and the first-party Magento pack use `PackService` for planning,
installation, configuration, upgrades, and removal. Connectors belong inside
the pack as GraphJin source declarations and bundled OpenAPI specifications.
Installing a pack does not install a plugin.

## Package

Use `apps/worker/test/fixtures/service-health` as a small connector-pack example.
Keep `pack.yaml` and its declared artifacts inside one directory whose name
matches `metadata.id`. Give each changed release a new manifest version.
From the directory containing your pack, create the archive:

```sh
zip -r service-health.zip service-health
```

The ZIP must contain `service-health/pack.yaml`, not a root-level `pack.yaml`.
Include declared empty directories too. Only non-executable UTF-8 YAML, JSON,
GraphQL, and Markdown files are accepted. Links, nested archives, duplicate or
ambiguous paths, and encrypted entries are rejected. Limits are 16 MiB compressed,
64 MiB extracted, 8 MiB per file, 1,000 entries, 16 path components, and a maximum
100:1 expansion ratio per entry. First-party IDs, including `magento`, cannot
be replaced by uploads.

Custom API connectors support bundled OpenAPI with local references and optional
bearer authentication. Credentials must use declared `{{secret.<key>}}`
references; never put credentials in the archive. A pack may declare a
customer-owned OAuth client, reviewed consent scopes, and required network hosts.
OpenNeko handles browser consent and token refresh without installing a plugin
or executable connector. API write access requires explicit source capability,
action, and policy declarations. Write policies install disabled.

## Upload, review, and install

Configure an enabled organization GraphJin data source first. Then:

```sh
openneko pack upload ./service-health.zip
openneko secrets set pack.service-health SERVICE_API_TOKEN
openneko pack install service-health \
  --input service.base_url=https://provider.example.com \
  --secret-ref service.api_token=SERVICE_API_TOKEN \
  --source-id YOUR_DATA_SOURCE_ID --yes
openneko pack status service-health
openneko pack doctor service-health
```

The secret command prompts for the value without echoing it. Upload only stages
content; it does not activate artifacts. `--yes` approves the displayed fresh
review. For separate approval, use `pack review` with the same configuration,
then supply its `reviewHash` to `pack install --review-hash HASH --version VERSION`.
Approval covers the actor, organization, exact content, configuration, resolved
credentials, source bindings, and current artifact plan. Changes require review
again. An identical upload is idempotent; a changed existing version is rejected.

The Admin → Settings → Packs page provides the same upload, configuration,
review, and install flow. Source references containing only a database name and
kind require an existing read-only source binding. CLI users supply
`--bind SOURCE_ARTIFACT_KEY=EXISTING_GRAPHJIN_SOURCE_NAME` and `--source-id` to
select the GraphJin endpoint. Borrowed sources remain administrator-owned.

## Configure, update, and remove

```sh
openneko pack configure service-health --input service.timezone=UTC --yes
openneko pack upload ./service-health-0.2.0.zip
openneko pack upgrade service-health --version 0.2.0 --yes
openneko pack uninstall service-health
```

Configure uses the installed version, even when a newer upload exists. Upgrade
checks ownership and refuses to overwrite operator changes. Uninstall disables
owned automation and connector access while retaining business and execution
history. It preserves borrowed sources and refuses conflicting operator edits.

Validated uploads live beneath the existing OpenNeko configuration directory at
`agents/orgs/<encoded-org>/packs`, alongside immutable version provenance. The
existing encrypted config backup includes this storage. Preserve the metadata
database and configuration/secrets together when restoring an installation.
Interrupted staging is not discoverable; identical upload retries can publish a
completed orphan version. Apply compensates caught failures, but is not a
crash-resumable transaction across PostgreSQL, files, and GraphJin.

For Magento prerequisites, discovery, analytics credentials, and optional
Integration tokens, see [the Magento pack guide](../packs/magento/README.md).
