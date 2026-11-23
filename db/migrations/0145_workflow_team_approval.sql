-- A workflow with no owner runs as service only when a pack installed it or
-- an admin approved it as a team workflow. Rows from before this have neither
-- and wait for an admin to assign an owner.

alter table workflow_definition
  add column if not exists team_approved_by text,
  add column if not exists team_approved_at timestamptz;
