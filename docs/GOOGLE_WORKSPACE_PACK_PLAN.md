# Google Workspace REST pack plan

Status: the executable pack design has been removed. Google Workspace is not
implemented. This plan replaces the previous seven-step gws plan.

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
- `apps/worker/src/packs/declarative.ts` installs custom API sources with bearer
  authentication. It restricts those sources to reads and rejects custom write
  action artifacts. Magento has separate governed write adapters.
- A REST source alone does not prove browser OAuth, account isolation or token
  refresh. The removed account implementation depended on executable connectors;
  it cannot be retained unchanged.
- The previous gws skills contain CLI instructions. The unfinished import has
  been removed. REST-based skills must describe the supported GraphJin operations.

## Logical commits

1. Remove executable pack support and replace the plan.
   Revert the runtime, executable connector declarations, connector account
   routes/screens and connector action dispatch. Remove the unfinished gws pack.
   Verify the retained declarative and Magento installation paths.

2. Verify the Google REST and authentication path.
   Trace the installed GraphJin version's API source authentication and the
   existing OpenNeko source-secret handling. Prove a Google REST read with a
   customer-owned OAuth client. Identify the minimum support needed for browser
   consent, token refresh and explicit account selection. Explain any required
   platform change before editing it. Do not use plugin registration or a
   pack-supplied executable as a shortcut.

3. Add the declarative Google Workspace read pack.
   Add the manifest, OpenAPI specs, sources, saved queries, read workflows, skills
   and setup instructions. Use the existing installer. Verify installation,
   real Google reads, refresh, restart and uninstall. Record service permissions
   and account restrictions. Do not claim unsupported services work.

4. Add governed Google REST writes.
   First verify the existing GraphJin mutation and action approval paths. Reuse
   those paths and explain any missing generic support before changing platform
   code. Bind the selected account and complete payload to approval. Verify
   provider results and prevent repeated uncertain writes.

5. Verify cross-pack use and complete coverage.
   Test the Gmail price-email to Magento SKU-update task through Work. Build the
   requested broad Workspace coverage with REST-based skills and workflows.
   Record the supported operations and any remaining API or permission limits.

Commit each completed step after its relevant checks pass. A source declaration,
a simulated provider response or an installed skill is not live acceptance.
Never store client secrets, tokens or private mailbox content in this repository.

## Removal checks

The removal restores implementation code to commit `b58e1a7`. Only the pack
author instructions and this plan differ from that state. All 35 pack tests and
10 worker declarative/lifecycle tests pass, including the Magento lifecycle.
Worker and web type checks pass. The temporary gws registry and images were
removed. The customer OAuth JSON remains locally excluded from Git.
