# Google Workspace REST pack plan

Status: the executable pack design has been removed. The REST pack now has an
installable manifest and a separate pack-owned OAuth path. Broader REST reads, read workflows, and governed write definitions are now present. Live Google acceptance and the cross-pack test remain.

## Boundaries

Packs contain declarative artifacts: skills, workflows, policies, metrics, saved
queries and API source definitions. Packs do not install or run executables,
container images, CLIs or dependencies. No gws package or registry publication
is required.

Keep the generic declarative installer from issue #290 and the support for packs
without GraphJin. Keep existing published plugins independent and unchanged.
Keep the removal of mock execution and dry-run mode.

Google Workspace uses Google REST APIs. Declare OpenAPI sources and saved queries
for GraphJin, using the existing Magento source layout as a reference. Keep
Google-specific API definitions and skills under `packs/google-workspace/`.

## Current evidence and limits

- Magento declares an API source with an OpenAPI spec and bearer authentication
  in `packs/magento/graphjin/sources.yaml`.
- The generic installer accepts API sources that request `api.write` or
  `api.delete` when they set `read_only: false`.
- Generic pack action artifacts are installed as pack-owned action definitions.
  Work exposes them through the governed action and approval path without using
  the plugin registry. Write policies install disabled.
- Plugin OAuth remains independent. Packs now have a separate declarative OAuth
  flow with encrypted credentials, refresh, account selection, and source
  binding.
- `packs/google-workspace/` has an installable manifest, six API sources, twenty-four reviewed reads, nineteen governed writes, one installation query, six skills, two read workflows, four action groups, and one write policy.
- Gmail, Calendar, Drive, and Sheets request write access at the source. Only declared operations are exposed to the isolated pack executor role. The write policy installs disabled. Docs and Slides remain read-only.
- The upstream `gws` skills contain CLI instructions. REST-based skills must
  cover the same supported tasks through reviewed Google API operations without
  copying CLI commands.

## Logical commits

1. Correct the plan for the current generic pack runtime.
   Record the existing REST draft, generic write-source support and generic
   governed pack actions. Keep plugin OAuth and pack OAuth separate. Make no
   runtime or pack changes in this commit.

2. Add generic pack-owned OAuth.
   Let an administrator configure a customer-owned Google OAuth client. Build
   the callback URL from OpenNeko's public URL. Store tokens encrypted, refresh
   access tokens, bind the selected Google account to the installed pack and
   remove credentials on uninstall. Do not register a plugin or run a
   pack-supplied executable.

3. Complete the declarative Google Workspace read pack.
   Add the manifest, OAuth requirements, reviewed scopes, saved queries, read
   workflows and setup instructions. Validate every current operation against
   GraphJin 3.20.75 and a real Google account. Verify installation, reads, token
   refresh, restart and uninstall. Do not claim unsupported services work.

4. Add governed Google REST writes.
   Add reviewed mutation operations, pack action definitions and policies for
   Gmail, Drive, Calendar, Sheets and Docs. Keep Slides read-only. Bind the
   selected account and complete payload to approval. Install write policies disabled. Verify
   provider results, reconciliation, idempotency and supported undo operations.

5. Verify cross-pack use and complete coverage.
   Map the applicable upstream `gws` task catalog to reviewed REST operations,
   skills and workflows. Test the Gmail price-email to Magento SKU-update task
   through Work. Record supported operations and remaining API or permission
   limits.

Commit each completed step after its relevant checks pass. A source declaration,
a simulated provider response or an installed skill is not live acceptance.
Never store client secrets, tokens or private mailbox content in this repository.

## Removal checks

The removal restores implementation code to commit `b58e1a7`. Only the pack
author instructions and this plan differ from that state. All 35 pack tests and
10 worker declarative/lifecycle tests pass, including the Magento lifecycle.
Worker and web type checks pass. The temporary gws registry and images were
removed. The customer OAuth JSON remains locally excluded from Git.
