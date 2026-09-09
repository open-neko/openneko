# Authoring solution packs

A solution pack bundles sources, API specifications, saved queries, metrics,
workflows, watchers, actions, policies, and skills for an application or business
use case. OpenNeko installs these artifacts through the shared pack lifecycle.
Packs that need connectors ship their own declarations and specifications; they
do not install runtime plugins.

For operator instructions, see [installing and upgrading custom packs](docs/CUSTOM_PACKS.md).
For contributing code, see [CONTRIBUTING.md](CONTRIBUTING.md).

## Start from an example

- [Service health](apps/worker/test/fixtures/service-health/pack.yaml) is a small
  custom pack with an API connector, query, metric, workflow, watcher, and skill.
- [Magento](packs/magento/pack.yaml) is the first-party commerce pack. Its
  [README](packs/magento/README.md) explains application-specific prerequisites.

Use the small example for new custom packs. Magento includes governed write
adapters implemented in OpenNeko; copying its action YAML does not make those
adapters available for another application.

## Directory layout

The manifest declares artifact paths, so directory names below are conventions,
not implicit discovery rules. All declared paths must exist and stay within the
pack root. Omit artifact paths for types the pack does not use. A declared empty
directory must still exist in the archive.

For a skills-only pack, declare `artifacts: { skills: [skills/my-skill] }`. Omit
`artifacts.graphjin` and `compatibility.graphjin`; `compatibility.applications`
and `compatibility.databases` can be empty arrays. Use empty health-check arrays
and an empty readiness map. This pack needs no data connection or GraphJin
configuration. GraphJin artifacts require GraphJin compatibility declarations.

To remove all GraphJin artifacts from an installed pack, uninstall it first.
This stops access through its owned sources before a new version is installed.

```text
my-pack/
├── pack.yaml
├── README.md
├── graphjin/
│   ├── sources.yaml
│   ├── relationships.yaml
│   ├── specs/
│   │   └── my-api.yaml
│   └── queries/
│       └── health.gql
├── metrics/
│   └── health.yaml
├── workflows/
│   └── review.yaml
├── watchers/
│   └── unhealthy.yaml
├── actions/
├── policies/
└── skills/
    └── my-pack-review/
        └── SKILL.md
```

YAML artifact directories accept `.yaml` and `.yml`; saved queries accept `.gql`
and `.graphql`. Each skill has an explicitly listed directory and `SKILL.md`.

## Manifest

Copy a working `pack.yaml` and edit these sections:

| Section | What to declare |
| --- | --- |
| `apiVersion`, `kind` | `openneko.app/v1` and `SolutionPack` |
| `metadata` | Stable lowercase-slug `id`, display `name`, semantic `version`, lowercase-slug `publisher` and `category` |
| `compatibility` | OpenNeko and GraphJin versions, supported application editions/versions, and database engines/versions |
| `inputs` | Named settings with a type: `string`, `url`, `integer`, `enum`, `timezone`, or `boolean`; defaults and required values as appropriate |
| `secrets` | Secret keys, purpose, and whether required; never credential values |
| `artifacts` | Source/relationship files, spec paths, query and YAML directories, and skill directories |
| `health` | Required preflight checks, readiness groups, post-install steps, and post-write canaries |

Custom inputs should have defaults or be supplied by the installer. Do not assume
Magento's application-specific discovery runs for a new pack. Declaring a health
check name also does not implement a new runtime check.

The [manifest schema](packages/packs/src/manifest.ts) is the authoritative field
reference. Unknown manifest fields are rejected.

## Artifacts and stable identities

Structured metrics, workflows, watchers, actions, and policies declare `key` and
`targetRef`. For example, a metric can use `key: metric.health` and
`targetRef: my-pack.healthy`. Keep identities stable across versions: the installer
uses them to track ownership and changes. Namespace installed targets to avoid
collisions with other packs or operator-created artifacts.

| Artifact | Authoring guidance |
| --- | --- |
| Sources | Declare API connectors or references to administrator-owned database sources in `sources.yaml`. |
| Relationships | Declare GraphJin relationships in the manifest's relationship file; use the example's empty declaration when none are needed. |
| OpenAPI specs | Describe the provider's actual paths, HTTP methods, parameters, bodies, and responses. Use local references. |
| Saved queries | Give queries stable filenames. Metric execution refers to the query name without its extension. |
| Metrics | Define presentation, cadence, source, saved query, result extraction, and freshness. See the [example metric](apps/worker/test/fixtures/service-health/metrics/health.yaml). |
| Workflows | Define a goal, output contract, and optional schedule with a timezone input. See the [example workflow](apps/worker/test/fixtures/service-health/workflows/health.yaml). |
| Watchers | Reference a workflow artifact key and define a query, value path, threshold, cadence, debounce, cooldown, and severity. |
| Skills | Write Markdown instructions with `name` and `description` frontmatter. Explain data sources, expected outputs, and permitted actions. |
| Actions and policies | Use supported runtime contracts. A declaration alone cannot implement a new execution adapter. |

See the [artifact schemas](packages/packs/src/artifact-schema.ts) for exact fields
and the [bundle loader](packages/packs/src/bundle.ts) for identity and reference
validation. Avoid renaming a query file or skill casually: filenames and skill
names participate in artifact identity.

## Connectors and credentials

A custom API source can declare bearer authentication like this:

```yaml
sources:
  - name: my_pack_api
    kind: api
    base_url: "{{my-pack.base_url}}"
    openapi: graphjin/specs/my-api.yaml
    auth:
      type: bearer
      token: "{{secret.my-pack.api_token}}"
```

Declare `my-pack.base_url` in `inputs` and `my-pack.api_token` in `secrets`, with
purpose `graphjin_api_auth`. The installer supplies configuration and resolves
secret references through the encrypted secret store. Keep credentials out of
Git, archives, skills, and example payloads.

Custom source access is currently read-only. Executable connector code, OAuth
refresh, and custom write adapters are not supported. A spec containing write
operations does not grant permission to execute them. Magento uses the same pack
lifecycle but retains its existing application-specific governed write adapters.

Database source references require an existing read-only source binding and an
organization GraphJin data source. See the [installation guide](docs/CUSTOM_PACKS.md)
for `--source-id`, `--bind`, and secret-reference options.

## Validate and exercise the pack

From the repository root, with workspace dependencies installed, validate the
small example through the same bundle loader used by installation. Create its
empty artifact directories first; Git does not preserve empty directories:

```sh
mkdir -p apps/worker/test/fixtures/service-health/{actions,policies}
pnpm --filter @neko/worker exec tsx -e 'import { loadSolutionPack } from "@neko/packs"; loadSolutionPack(process.argv[1]).then(b => console.log(b.manifest.metadata.id, b.artifacts.length)).catch(e => { console.error(e.message); process.exitCode = 1; })' ./test/fixtures/service-health
```

Replace the final path with your pack directory. Relative paths in this command
resolve from `apps/worker`; use an absolute path for a pack in another repository.

Bundle validation checks structure and references. It does not prove provider
compatibility or successful end-user execution. Before submitting a change:

1. Add focused regression coverage for changed behavior, using the existing pack
   and worker test suites.
2. Upload and install in an isolated stack; review the plan and readiness results.
3. Run affected saved queries, metrics, workflows, and watchers against the actual
   provider. Check persisted results, not just successful HTTP responses.
4. For supported writes, check the provider state, applied receipt, approval,
   reconciliation, and any advertised undo behavior. Use dedicated test records.
5. Exercise upgrade behavior, including locally modified installed artifacts.

Relevant suites include `pnpm --filter @neko/packs test` and the worker's
`pack-declarative`, `pack-connector.integration`, `pack-lifecycle.integration`, and
`pack-uploads` tests. Integration tests need their documented test dependencies;
a skipped test is not a live pass.

## Save changes, submit a PR, and release

The pack's Git source is the reusable authoring source of truth. Edit its files,
validate and live-test the affected behavior, increment `metadata.version` for a
changed release, and submit a PR to the repository that owns the pack. Describe
the resulting behavior, compatibility changes, and validation evidence.

For a custom pack, publish a ZIP containing `<pack-id>/pack.yaml` and the declared
artifacts. Users upload the new version, review it, and upgrade. Changed content
cannot replace an already uploaded version. See [archive limits and release
commands](docs/CUSTOM_PACKS.md).

Magento is maintained under `packs/magento/` in this repository. Submit Magento
pack changes here; reserved first-party IDs cannot be replaced by custom uploads.
Merged first-party changes must be included in a shipped OpenNeko build before
users can upgrade to them.

Work chat can save individual workflow and skill changes, but does not write
those changes back into a versioned pack bundle. Installed customizations are
preserved as local changes; pack upgrades refuse conflicting edits. To share a
customization, port it into the pack's Git source and submit a PR. There is
currently no in-app pack editor or automatic export of these edits as a release.

## Executable connectors

A pack can declare `connectors` in `pack.yaml`. Each connector runs in its own
OpenShell sandbox. It does not require an installed plugin. The worker must have
access to an OpenShell gateway and to the image registry.

```yaml
connectors:
  - id: example
    image: registry.example.com/example@sha256:<actual-image-digest>
    entrypoint: /app/connector
    operations:
      - id: lookup
        description: Look up a record
        effect: read
    network:
      - host: api.example.com
        port: 443
        binary: /usr/local/bin/node
```

Replace the example image with a published image and its full SHA-256 digest.
Floating tags are rejected. Installation review starts the image and checks the
entry point. This checks availability and whether the image can run on the
worker's sandbox host. Review shows the image, operations and network access.
The digest identifies the approved image; it does not certify its publisher.

The image must supply an executable entry point, `/usr/bin/test`, a `sandbox`
user and group, and a writable `/sandbox` directory. Include all connector code
and dependencies in the image. Use the test image under
`apps/worker/test/fixtures/connector-image/` as a small working example.

The entry point receives one argument: the path to a private JSON request file.
The file contains `{ "operation": "lookup", "input": {} }`. Write one JSON value
to standard output. Do not write progress messages to standard output or secrets
to logs. Requests and output each have a 1 MiB limit. Execution has a 60-second
limit, one CPU and 512 MiB of memory. The sandbox is deleted after each call.
Network access requires an exact host, port and executable path in the declaration.
A script must declare its interpreter's path for network access.

Keep image source and build files in the pack repository. Build and publish the
image first. Put its digest in the manifest. Create the installable ZIP from only
`pack.yaml` and its permitted declarative artifacts. Do not include Dockerfiles,
executables or dependency directories in the ZIP. The worker does not build images.

Execution checks the installed pack and its approved content before each call.
Upgrade and removal use the existing pack lock and wait for active execution.
The current internal service supports declared reads only. Work access, approved
writes and browser account connections are separate implementation steps.

## Browser account connections

An executable connector can add an `auth` declaration:

```yaml
auth:
  label: Example service
  authorizationOrigin: https://accounts.example.com
  scopes: [records.read]
  credentialVersion: "1"
```

The customer supplies the OAuth client ID and secret on **Integrations → Pack
accounts**. Only an administrator can change these settings. Each user then
connects their own accounts. The account selector supports reconnect and
disconnect. Users can connect more than one account. Solo mode has its own owner;
its accounts do not become a signed-in user's accounts when SSO is enabled.

Register this callback with the provider, using your OpenNeko site's origin:
`/api/pack-accounts/<pack-id>/<connector-id>/callback`.
Use HTTPS outside localhost development. The connector must support authorization
codes with PKCE S256. Its authorization URL must use the declared origin, client
ID, callback, state and scopes. The platform rejects extra scopes and changed
callback parameters. State expires after ten minutes and can be used once. The
callback also requires the same browser cookie and authenticated account owner.

Connection requests use the same private file and sandbox as normal execution:

```json
{"connection":"authorize","input":{"client":{"clientId":"...","clientSecret":"..."},"redirectUri":"...","state":"...","codeChallenge":"...","scopes":["records.read"]}}
```

Implement these four requests in the connector:

| Request | Input in addition to `client` | Required result |
| --- | --- | --- |
| `authorize` | `redirectUri`, `state`, `codeChallenge`, `scopes` | `{ "authorizationUrl": "https://..." }` |
| `exchange` | `code`, `redirectUri`, `codeVerifier`, `scopes` | Credential object below |
| `refresh` | `credential` | Complete replacement credential, including rotated refresh tokens |
| `revoke` | `credential` | `{ "revoked": true }` only after the provider confirms revocation |

The credential object has `accountId` (stable provider identity), `label`,
`scopes` (actually granted), `expiresAt` (Unix milliseconds) and `tokens` (an opaque
JSON object). The connector must validate provider responses and account identity.
It must fail on provider errors. Do not report requested scopes as granted without
checking the response. The platform rejects partial consent and a different
provider identity during reconnect or refresh.

Client settings and account credentials use the existing encryption code. They
are stored in separate pack tables linked to the installation. They are not
plugin credentials or named source secrets. Listing accounts returns only account
IDs, labels and status. The platform passes credentials to the selected connector
through its private request file; it does not return them to the browser or agent.

Internal execution of an authenticated operation requires both `ownerId` and
`accountId`. The caller must supply the authorized execution user. Background work
must preserve that explicit binding; there is no default or fallback account.
The service checks ownership before it supplies a credential. Work and workflow
exposure still belong to the action-dispatch step.

Refresh starts within 60 seconds of expiry and uses the existing pack lock. A
successful refresh is saved before execution. An uncertain or failed refresh
requires reconnect; the platform does not retry an old rotating refresh token.
Disconnect keeps credentials for retry if revocation is not confirmed.

Compatible image upgrades keep accounts. Changes to the authentication declaration
(including scopes or `credentialVersion`) require new client setup and consent.
Client setting changes remove saved accounts and pending callbacks. Uninstall
removes these records in the existing installation transaction. Disconnect first
if you also want to revoke the provider's grant before uninstall.

Storage choice: `data_source_secret` is organization-scoped and has no account
owner field. The plugin store has a different ownership contract. Migration 0071
therefore adds `pack_connection_client` and `pack_account`, with cascading foreign
keys to the existing installation. It does not add another pack registry.

Security reference: [OAuth security best current practice](https://www.rfc-editor.org/rfc/rfc9700.html).
