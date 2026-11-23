-- The OpenShell gateway is gone. Its login role held the application role's
-- rights, so an upgraded install disables the login and takes the rights back.
-- Its old tables stay in place.

do $$
begin
  if exists (select from pg_roles where rolname = 'openshell') then
    alter role openshell nologin password null;
    execute format('revoke %I from openshell', current_user);
  end if;
exception when insufficient_privilege then
  raise warning 'could not disable the openshell role: %', sqlerrm;
end $$;
