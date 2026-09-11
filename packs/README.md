# Solution packs

## Installation and configuration

Built-in packs install in one click from Admin, Settings, Packs. Installation registers the included definition without contacting external services or requiring credentials. The installed pack then exposes fields from `inputs`, `secrets`, and `oauth`. Reviewing and applying those settings activates its artifacts through the existing lifecycle. Until then, readiness reports that configuration is required. Uploaded packs retain the archive review and installation flow.

An optional `management: { label, path }` declaration links to an existing local operational screen once configuration is applied. Ordinary configuration and OAuth setup always use the shared renderer.

## Pack-owned connection screens

Declare OAuth connections in `pack.yaml`. The generic Admin and Integrations
screens render the metadata; adding a provider requires no OpenNeko UI code.

```yaml
oauth:
  - key: account
    providerLabel: Example service
    scope: user # deployment retains the existing shared-account flow
    experience:
      description: Connect your account for your work and automations.
      setupInstructions: Create an OAuth web client and register the displayed callback URI.
      helpUrl: https://example.com/oauth-help
    # Also declare authorizationUrl, tokenUrl, userInfoUrl, clientIdInput,
    # clientSecret, accessToken, refreshToken, and the reviewed scopes.
```

`experience` is optional plain text, with an HTTPS help link. OpenNeko owns the
standard controls, accessibility, session binding, PKCE, encryption, and token
refresh. Packs supply content and reviewed provider endpoints, not executable UI
or scripts. `accountIdField` and `accountLabelField` select provider user-info
fields (defaults: `sub` and `email`).

For `scope: user`, source bearer tokens still reference the declared
`{{secret.<accessToken>}}` key. Installation materializes a dedicated request
header instead of a static token; users never paste tokens into pack settings.
User-scoped connections use the OAuth client configured by an admin. Personal
workflows execute as their active owner, and action approvals bind to the
requester's connection revision. Ownerless runs have no personal credentials.
