# Google Workspace solution pack

This pack uses Google Workspace REST APIs through declarative GraphJin sources.
It does not install the `gws` CLI, executables, containers, plugins, or package
dependencies.

The read surface covers Gmail, Drive, Calendar, Sheets, Docs, and Slides.
The OpenAPI specifications expose reviewed operations only. The customer owns
the Google Cloud project and OAuth client. OpenNeko does not use a plugin or run
a connector executable.

## Connect an account

1. In Google Cloud, enable the Workspace APIs listed in `pack.yaml`.
2. Configure the OAuth consent screen.
3. Create a Web application OAuth client.
4. Open Admin, Settings, Packs and select Google Workspace.
5. Copy the redirect URI shown under Google Cloud setup into the OAuth client.
6. Enter the client ID and client secret, then choose Connect account.
7. Select the Google account and grant the listed permissions.
8. Review and install the pack.

OpenNeko calculates the redirect URI from `OPENNEKO_PUBLIC_URL`. If that setting
is absent, it uses the URL that opened the Admin page. This supports custom
ports and hosted domains without a fixed callback address.

The client secret, access token, and refresh token are encrypted in the pack's
secret store. OpenNeko refreshes the access token before it expires and updates
the installed GraphJin sources. Disconnecting blocks those sources. Removing
the pack deletes its stored credentials.

The upstream Google Workspace CLI skill catalog is used as a task-coverage
reference. Its CLI commands are not copied into this pack. Each adopted task is
rewritten against the pack's reviewed REST operations and OpenNeko approval
rules.

## Current coverage

- Curated REST reads: Gmail messages, threads, drafts, labels, history and attachments; Drive files, permissions and account storage; Calendar lists and events; Sheets metadata and values; Docs and Slides content.
- Read skill for service-specific requests.
- Cross-application research skill for evidence gathering and handoff to other
  installed packs.
- Gmail account profile query used as the installation check.
- Read workflows for cross-application research and Gmail plus Calendar briefings.
- Governed Gmail, Calendar, Drive, and Sheets actions. Their write policy installs disabled and an administrator must enable it before use.

## Write coverage

- Gmail: create drafts, send messages, and manage labels.
- Calendar: create, replace, and delete events.
- Drive: create, copy, move, rename, and manage sharing. Metadata operations are supported; binary upload is not.
- Sheets: update, append, clear, and batch-update values.
- Slides remains read-only.
