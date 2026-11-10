-- openneko serve connects as neko_files. It logs in only after
-- `openneko serve-init` sets its password, derived from the secret key.
do $$
begin
  if not exists (select from pg_roles where rolname = 'neko_files') then
    create role neko_files nologin;
  end if;
exception when insufficient_privilege then
  raise warning 'could not create the neko_files role: %', sqlerrm;
end $$;

do $$
begin
  if exists (select from pg_roles where rolname = 'neko_files') then
    grant usage on schema public to neko_files;
    grant select, insert, update, delete on file_content_file, file_content_unit, file_content_table to neko_files;
    grant usage, select on sequence file_content_unit_id_seq to neko_files;
  end if;
end $$;
