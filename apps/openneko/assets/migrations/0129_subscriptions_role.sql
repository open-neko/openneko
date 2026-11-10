-- The subscriptions GraphJin in openneko serve connects as neko_subscriptions.
-- It logs in only after `openneko serve-init` sets its password.
do $$
begin
  if not exists (select from pg_roles where rolname = 'neko_subscriptions') then
    create role neko_subscriptions nologin;
  end if;
exception when insufficient_privilege then
  raise warning 'could not create the neko_subscriptions role: %', sqlerrm;
end $$;

do $$
begin
  if exists (select from pg_roles where rolname = 'neko_subscriptions') then
    grant usage on schema public to neko_subscriptions;
    grant select on workflow_output to neko_subscriptions;
  end if;
end $$;
