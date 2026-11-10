-- Library uploads now live in each user's private folder of openneko serve.
-- The worker moves older uploads there at boot and marks a missing file failed.

drop table if exists library_event;
drop table if exists library_concept;

drop index if exists library_document_async_recovery_idx;
alter table library_document
  drop column if exists skip_reason,
  drop column if exists extract_checkpoint,
  drop column if exists extracted_relative_path,
  drop column if exists extracted_content_hash,
  drop column if exists extractor_fingerprint,
  drop column if exists extracted_at,
  drop column if exists distill_checkpoint,
  drop column if exists distilled_at;

alter table library_document drop constraint if exists library_document_status_check;
update library_document set status = 'ready' where status not in ('ready', 'failed');
alter table library_document
  add constraint library_document_status_check check (status in ('ready', 'failed'));
alter table library_document alter column status set default 'ready';

delete from item_grant where item_type in ('library_collection', 'library_concept');
alter table item_grant drop constraint if exists item_grant_item_type_check;
alter table item_grant add constraint item_grant_item_type_check check (item_type in (
  'skill', 'workflow', 'metric', 'dashboard',
  'watcher', 'team_memory', 'data_source', 'saved_query', 'api_operation', 'action',
  'integration', 'channel', 'pack'));

create or replace function seed_everyone_item_grants(p_org_id text) returns void as $$
begin
  insert into item_grant (org_id, group_id, item_type, item_id)
  select g.org_id, g.id, t.item_type, '*'
  from user_group g
  cross join (values
    ('skill'), ('workflow'), ('metric'), ('dashboard'),
    ('watcher'), ('team_memory'), ('data_source'), ('saved_query'), ('action'),
    ('integration'), ('channel'), ('pack')) as t(item_type)
  where g.org_id = p_org_id and g.slug = 'everyone'
  on conflict do nothing;
end;
$$ language plpgsql;
