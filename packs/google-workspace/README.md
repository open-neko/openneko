# Google Workspace solution pack

This pack uses Google Workspace REST APIs through declarative GraphJin sources.
It does not install the `gws` CLI, executables, containers, plugins, or package
dependencies.

The first read surface covers Gmail, Drive, Calendar, Sheets, Docs, and Slides.
The OpenAPI specifications are curated to expose only reviewed operations. The
pack will use a customer-owned Google Cloud OAuth client. Browser consent,
refresh-token storage, account selection, and source binding must be complete
before the pack is published as installable.

The upstream Google Workspace CLI skill catalog is used as a task-coverage
reference. Its CLI commands are not copied into this pack. Each adopted task is
rewritten against the pack's reviewed REST operations and OpenNeko approval
rules.

## Current implementation

- Curated read specifications: Gmail, Drive, Calendar, Sheets, Docs, Slides.
- Read skill for service-specific requests.
- Cross-application research skill for evidence gathering and handoff to other
  installed packs.

## Still required before installation

- Customer-owned OAuth connection and refresh.
- Source declarations bound to the selected account.
- Saved GraphJin queries verified against the released GraphJin version.
- Read workflows and live tests for every declared operation.
- Governed write operations and approval tests.
- Additional Workspace services needed for the full upstream skill catalog.
