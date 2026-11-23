# Context repository (removed)

OpenNeko no longer keeps skills, workflows and memories in a git repository.
Each organization's skills are plain folders, and OpenNeko keeps no old versions.

What replaced it:

- **Change record.** Each change to a skill, a workflow or a team memory adds a
  `config_change` row. The row records who made the change, what changed and when.
- **Skill edits.** An administrator's change to a team skill waits for approval
  at **Admin → Settings → Approval rules**. A member's change applies to their own
  copy only.

On the first start after the upgrade, `openneko web` moves each organization
folder off its old repository. It keeps the old repository beside the folder as
`.git.pre-plain`. To run the move by hand, use
`openneko migrate config-repo`. Add `--dry-run` to list the changes first.
