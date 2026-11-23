-- config_change rows stay as the audit record; their commit_sha column keeps
-- the ids of past commits.
drop table if exists config_ref;
alter table skill_usage drop column if exists config_commit_sha;
alter table memory_fork drop column if exists baseline_sha;
