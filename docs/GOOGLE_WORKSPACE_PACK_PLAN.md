# Google Workspace pack implementation plan

Status: steps 1 and 2 are complete and verified. Steps 3 through 7 are
not implemented. The imported Google Workspace skills, license and inventory
remain uncommitted. They are not evidence of a working connector. Mock execution
and dry-run mode were removed separately in commit `b2a39dc`.

## Outcome and boundaries

Ship `packs/google-workspace/` through the generic installer from issue #290.
The customer owns the GCP project and OAuth Web application. Users connect in
OpenNeko; they do not install a plugin, run CLI login or export tokens.

Packs and plugins remain independent. Published plugins keep their existing
contracts, routes and behavior. Pack connectors must not depend on plugin types,
PluginRegistry, the plugin installer, plugin credential storage or plugin runtime.
Do not register packs as plugins or pass pack providers through plugin endpoints.

Google-specific code, endpoints, scopes, method/helper handling, gws dependencies,
skills, image build files, tests and instructions belong under the pack directory.
Platform changes implement only the four prerequisites below. Explain any further
scope outside `packs/` before editing it.

## Verified existing foundations

Research used the committed #290 implementation, separately from the unfinished
draft. Existing code already supplies:

- `apps/worker/src/packs/service.ts`: review, install/configure/upgrade/uninstall,
  installation state, uploaded bundle pinning, artifact ownership, compensation,
  skill installation and secret cleanup. Extend these paths; no second registry
  or lifecycle. Embedded bundle identity needs checking before executable dispatch;
  uploaded content pinning alone is not proof that both paths are immutable.
- `packages/db/src/schema.ts`: `pack_install`, `pack_artifact`,
  `pack_action_definition` and organization-scoped encrypted source secrets.
- `apps/worker/src/packs/action-preflight.ts`: installed action readiness gates.
- `packages/llm/src/workflows`: policies, approvals, action adapters and execution
  records independent of runtime plugins. The action tool builder's historical
  plugin-oriented name does not itself require plugin registration.
- `packages/secret-crypt`: independent encryption. Do not invent another cipher
  or assume a new table is needed before checking storage ownership and access.
- `apps/worker/src/packs/declarative.ts`: pack-owned read-only OpenAPI sources via
  GraphJin, including bearer authentication, without plugin registration.
- Existing OpenShell infrastructure for isolation. The current plugin runner is
  plugin-specific; pack execution must not import it.

Actual gaps: mandatory GraphJin artifacts/source selection; no executable pack
launch contract; rejection of custom action artifacts; browser account connection
routes that require a PluginRegistry entry. OAuth is not universally plugin-only:
Salesforce Records already implements independent client-credentials OAuth.

## Commit sequence

Make these commits in order. Commit each step after its checks pass and before
starting the next step. Each commit must include its tests and the author
instructions for the behavior it adds. Do not defer required tests to commit 7.
Keep existing plugins unchanged. Do not include unrelated changes.

### 1. `feat(packs): support packs without GraphJin`

- Complete the retained optional-artifact changes.
- Change manifest validation, loading, installation and configuration UI together.
- Skip GraphJin setup and checks only when the pack does not require GraphJin.
- Check install, configure, upgrade, restart and uninstall for a skills-only pack.
- Run the existing Magento and declarative pack tests.

Complete when a skills-only pack works without a data source. An upgrade must
not leave old GraphJin resources active without an owner.

### 2. `feat(packs): run pack-owned connectors in isolation`

Requires commit 1.

- Add pack connector declarations and a pack runner that uses OpenShell directly.
- Use a pack-owned image with a fixed digest. Include its permissions and identity
  in the existing installation review.
- Resolve the connector from the installed pack. Check the approved content for
  both uploaded packs and first-party packs.
- Add a small test pack with no Google code and no plugin dependency.
- Test execution, timeout, denied network access, missing image and pack removal.
- Document the image contract and how to build an installable archive.

Complete when the test pack runs in isolation without installed plugins. Do not
expose write operations to Work before commit 4 supplies the approval checks.

### 3. `feat(packs): support browser account connections`

Requires commit 2.

- Add pack connection routes and controls, with provider logic in the connector.
- Select credential storage after checking existing storage and access rules.
  Add a migration only if those rules require one.
- Implement account selection, OAuth state checks, encrypted storage, refresh,
  reconnect and disconnect as one complete connection flow.
- Use existing pack upgrade and uninstall operations for connection cleanup.
- Test two accounts, organization access, partial consent, callback replay,
  concurrent refresh, restart, changed client settings and uninstall.
- Check the UI on desktop and mobile. Run existing plugin connection tests.

Complete when a user can connect the test pack through the browser. Credentials
must stay available after restart and remain inaccessible to other accounts.

### 4. `feat(packs): execute declared operations through action approvals`

Requires commits 2 and 3.

- Accept supported connector action artifacts through the existing validator.
- Use existing action definitions, readiness checks, policies and execution records.
- Make pack operations available to Work and scheduled workflows directly.
- Check the pack version, account, payload and attachments before execution.
- Store provider results. Do not repeat a write when its result is uncertain.
- Test reads, approved writes, denied writes, changed approvals, explicit workflow
  account selection and queued work after uninstall.

Complete when the test pack can perform a read and an approved write through
Work, with no plugin registration and no mock execution.

### 5. `feat(google-workspace): package gws and account connection`

Requires commits 1 through 4. Keep changes under `packs/google-workspace/`.

- Add the manifest, connector image source and a verified image digest.
- Pin gws and its dependencies. Record licenses and source versions.
- Implement customer-owned Google Web-client OAuth and account identity.
- Add setup instructions and access groups. Test token exchange and refresh.
- Verify installation and a live account connection before calling this complete.

Complete when the pack installs through the generic installer and connects a
Google account. Image distribution must work; a local build alone is insufficient.

### 6. `feat(google-workspace): add governed operations and all gws skills`

Requires commit 5. Keep changes under `packs/google-workspace/`.

- Add the complete verified upstream skill inventory and required support files.
- Record changes to upstream login, installation and execution instructions.
- Use gws for API commands. Keep operation validation and required scopes in the pack.
- Support bounded file access and verify provider results after writes.
- Test method and helper permissions, account isolation and unknown operations.
- Identify the support required for watches and subscriptions before enabling them.

Complete when all imported skills have their dependencies recorded and declared
operations use the approved execution path. Record unsupported behavior explicitly.

### 7. `test(google-workspace): add live acceptance checks and results`

Requires commit 6. Keep the test scripts and reports under the pack directory.

- Add repeatable live checks for all skill families and record exact results.
- Test the Gmail price-email to Magento SKU-update task through Work.
- Verify restart, refresh, upgrade and uninstall with retained account state.
- Record any blocked API, account permission or license requirement.
- Complete the pack setup and capability documentation from verified results.

Complete only when the required checks pass. A blocked capability remains open;
a count of installed skills is not a passing result. Do not store credentials or
private mailbox content in test reports.

The plan update is not an implementation commit. Do not commit the current draft
as a complete feature. Assign each retained change to the commit above that owns it.

## Step 1 verification record

The skills-only lifecycle test uses PostgreSQL and files on disk. It creates an
organization with no data sources and removes the GraphJin configuration setting.
It checks review, install, configure, a new PackService instance, upgrade, doctor
and uninstall. No GraphJin query is made. A separate case rejects removal of
GraphJin declarations during upgrade and checks uninstall against stored state.

The browser test uses fixture API responses to check the UI. It does not replace
the database lifecycle test. It checks no data-source request, review failure and
retry, disabled controls, error focus and successful installation. Desktop width
is 1280 pixels; phone width is 390 pixels. Screenshots were inspected.

UI reuse map for the commit or PR:

| UI function | Existing component or style | Check |
| --- | --- | --- |
| Data-source selection | `Field`, `NativeSelect` | Hidden for skills-only packs; required for data packs |
| Review and install | `Button`, `ActionGroup` | Focus visible; disabled during review; retry works |
| Review text | Existing `text-ui-body-sm` style | States that no data connection is required |
| Phone layout | Existing grid and control styles | No horizontal overflow; controls at least 44 pixels high |

Files that contain the checks:

- `packages/packs/test/optional-artifacts.test.ts`
- `apps/worker/test/pack-lifecycle.integration.test.ts`
- `apps/worker/test/pack-declarative.test.ts`
- `apps/web/test/visual/skills-only-packs.spec.ts`

The existing pack tests, web unit tests, lint, type checks and design-system check
also form part of verification. Some tests first reached their time limits while
other suites were running. Failed tests were rerun separately. Database-dependent
web tests without a configured database were skipped; the pack lifecycle tests
used the local test database explicitly. Both packaged GraphJin tests passed.
The optional combined browser test in that suite was skipped; the dedicated
skills-only browser test passed.

Results: 35 pack tests, 5 database lifecycle tests, 5 declarative validation tests
and 2 packaged GraphJin tests passed. Web lint, web and worker type checks, and
the design-system check passed. The web suite had 331 passes and 108 skips on
its first run; its two failed files passed on retry (24 tests). The dedicated
browser test passed at both widths. Step 1 is committed as `feat(packs): support packs without GraphJin`.

## Four platform prerequisites

### 1. Packs without GraphJin

- [x] Make GraphJin and absent artifact directories optional in manifest/loading.
- [x] Require a data source and perform GraphJin configuration/preflight only for
      packs declaring GraphJin artifacts; include review, doctor and uninstall.
- [x] Address GraphJin compatibility requirements for packs without GraphJin.
- [x] Preserve existing Magento/declarative behavior and guard upgrades that would
      otherwise abandon previously installed GraphJin resources.
- [x] Verify a skills-only pack installs and uninstalls without a data source.

Expected scope: `packages/packs/src/{manifest,bundle}.ts`, existing PackService,
its generic configuration UI where data-source selection is currently mandatory,
and focused regression tests. Retained draft changes are partial and need review.

### 2. Pack-owned executable connectors

Implemented packaging: a pack-owned image with a fixed digest. The image contains
connector code and its dependencies. Source and build files stay in the pack
repository. The installable archive references the image. The worker does not
build code from an archive.

- [x] Define connector identity, entry point, operations, image and network access.
      Account authentication and credential delivery belong to commit 3.
- [x] Include connector declarations and image identity in review and content hashes.
- [x] Use OpenShell directly with bounded execution and a private request file.
      Do not load connector code into the worker. Reads are internal only;
      writes are blocked until commit 4 adds action approval checks.
- [x] Require a full image digest. Start the image during review and check its
      entry point. This checks availability and host compatibility. The digest
      identifies content; it does not certify the publisher.
- [x] Resolve connectors from installed pack state with no new registry.
- [x] Test embedded and uploaded packs through PostgreSQL and OpenShell, without
      plugin registration. Check non-root execution, changed content, denied
      writes and network access, timeout, restart, unavailable images and removal
      during active execution.
- [x] Document the image and archive contract in `PACKS.md`. Include a small
      executable test image and build instructions under the worker tests.

Scope: pack schema, existing review and installation state, one worker runner,
the existing review disclosure, author instructions and tests. No plugin runtime
changes, worker initialization changes or global gws installation were needed.

Verification: 36 pack tests and 3 runner tests passed. All 4 live execution tests
passed against PostgreSQL and OpenShell with a locally built image pinned to
`sha256:5bc8c90bf02dcdbebd19fc82bc8642838c15f39e557f31fa2d43df0705bd19e1`.
The review screen passed the browser check at widths of 1280 and 390 pixels,
including error, retry, focus, disabled controls and content wrapping.
The connector details reuse the existing `Disclosure` and permission list.
The 13 lifecycle, declarative and upload tests passed. Web tests passed with
340 tests and 108 skips. Worker and web type checks, web lint and `ui:check` passed.

### 3. Custom action dispatch

- [ ] Extend the existing custom-artifact validator for supported connector actions.
- [ ] Reuse `pack_action_definition`, readiness, policies, approvals and receipts.
- [ ] Dispatch to the installed pack's declared operation through the pack runner.
- [ ] Expose pack operations to Work and scheduled workflows directly; do not put
      them into plugin discovery endpoints. Reuse the platform action control plane.
- [ ] Bind writes to the reviewed definition/version, account, full payload and
      relevant attachment contents; revalidate at execution.
- [ ] Keep provider validation and result reconciliation in the pack. Unknown,
      changed, removed or unavailable operations fail; ambiguous writes must not
      be blindly retried or represented as successful.
- [ ] Test a read, approved write, denied write, stale approval and uninstall.

Expected scope: existing pack artifact validation/preflight/dispatch, worker and
Work/workflow discovery wiring, and tests. No replacement approval system or mock
fallback. Changes to action transport are limited to removing discovery coupling.

### 4. Browser connections owned by packs

- [ ] Add pack-specific start/callback/disconnect routes and manifest-driven UI
      using the existing design system. Preserve plugin routes unchanged.
- [ ] Bind expiring single-use state to organization, owner, installation and
      callback; enforce PKCE where supported and validate authorization on return.
- [ ] Run provider authorization, token exchange, refresh and revocation logic in
      the pack connector. Platform manages identity, encrypted persistence and
      delivery of credentials to that connector.
- [ ] Choose storage by checking existing access boundaries, account identity,
      concurrent refresh and lifecycle needs first. A new table is not presumed.
      Do not expose personal tokens through generic source-secret listing/resolution.
- [ ] Keep client configuration and account credentials scoped appropriately;
      invalidate affected connections when OAuth client settings change.
- [ ] Use existing installation state and uninstall compensation/cleanup paths.
      Preserve compatible connections across upgrades and require renewed consent
      for increased access. No separate connector lifecycle.
- [ ] Test account/org isolation, partial consent, replay, refresh rotation,
      reconnect, uninstall and explicit background workflow bindings.
- [ ] Verify desktop/mobile UI and existing published-plugin compatibility tests.

Expected scope: worker pack connection handlers and persistence, pack-specific web
routes/controls, lifecycle cleanup integration and focused tests. Any schema change
must be justified by the storage review, not introduced as scaffolding.

## Google Workspace pack work

After the prerequisites work for an unrelated executable test pack:

- [ ] Pin a released gws version, image/dependency digests and source provenance.
- [ ] Include all upstream skills, support files, license and an integrity inventory.
      Retained input is v0.22.5, commit
      `705fb0ecac6f4249679958f6325b809b63fdde17`, with 95 Markdown skill files.
      Reverify this pin; do not substitute a floating upstream release.
- [ ] Record adaptations to upstream install/login/command instructions for the
      pack connection and governed execution path, preserving relative links.
- [ ] Implement Google Web-client OAuth, service/access groups, actual granted
      scopes and account identity inside the pack. gws desktop localhost login
      alone does not implement the OpenNeko web callback experience.
- [ ] Use gws for API discovery, commands and pagination; avoid rebuilding those.
      Package credential isolation must prevent ambient ADC or account-cache fallback.
- [ ] Declare effects and scope requirements for methods/helpers. New Discovery
      methods do not silently gain write authorization. Skill prose is not a gate.
- [ ] Handle files through bounded artifact access. Watches/subscriptions require
      verified lifecycle/renewal support before being claimed operational.

User flow: install and review pack → configure customer OAuth client → choose
access and connect account → use Work → approve exact writes → verify results.
Personal accounts remain owner-scoped; shared automations require explicit access
and binding. Solo mode uses a stable authorized identity, not a default mailbox.

## Acceptance and documentation

- [ ] Regression: Magento and declarative packs retain install/upgrade/uninstall
      behavior; existing plugins operate unchanged.
- [ ] Independent executable test pack: no plugins or GraphJin required; connect,
      read, approve/write, restart, refresh, upgrade and uninstall.
- [ ] Workspace live checks: Gmail, Drive, Docs, Sheets, Slides, Calendar, Chat,
      Tasks and People representative reads and governed writes.
- [ ] Every remaining skill/helper/recipe/persona: record passed, failed or exact
      blocking API, license, permission or runtime prerequisite. Imported skills
      alone are not evidence of broad working coverage.
- [ ] Cross-pack Work test: find supplier Polo T-shirt price changes in Gmail,
      cite evidence, match exact Magento SKUs/currencies, preview, approve, execute
      and independently read back. Reject ambiguous/conflicting source data.
- [ ] Preserve a repeatable acceptance harness; update PACKS.md only for delivered
      contracts. Audit Google-specific code remains under `packs/google-workspace/`.

Live OAuth needs a customer-owned Web client, registered callback, consenting
accounts and enabled/licensed APIs. No live Google Workspace acceptance has passed.
Never place credentials in the repository, plan or test reports.

## References

- [Pack authoring](../PACKS.md)
- [Custom installation](CUSTOM_PACKS.md)
- [Google Workspace CLI](https://github.com/googleworkspace/cli)
- [Pinned skills](https://github.com/googleworkspace/cli/tree/705fb0ecac6f4249679958f6325b809b63fdde17/skills)
- [Google web-server OAuth](https://developers.google.com/identity/protocols/oauth2/web-server)
