# OpenNeko roadmap

Concrete follow-ups and integration gates for future work. This is not a complete
product backlog. Older local plans under the git-ignored `docs/` directory may
have stale implementation status; verify them against current source.

## RBAC-1: enforce effective access on every agent request

**Pending — required for fine-grained or multi-user access.**

Each run gets its own runner and run folder, so no run folder is reused across
users. Access still has to be enforced per request:

- Enforce RBAC on each data/tool/action request through server-side checks.
- Filter staged files and retrieved memories **before they reach the agent**.
  Existing org-wide knowledge staging needs review here.
- Cover all effective grants: sources and row/action restrictions, packs,
  channels, personal/team memories, uploaded files, role/group
  membership, ownership and sharing rules.

Acceptance: for each resource class above, revoke access and verify that the
next turn cannot retrieve the revoked content, including revocation during an
active turn.
