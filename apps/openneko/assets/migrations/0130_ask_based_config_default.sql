-- Ask-based config is on by default from this release. Organizations that
-- exist now keep it off until an admin turns it on.
insert into llm_provider_config (org_id, scope, provider, enabled, config)
select id, 'graphjin-config', 'graphjin-config', false, '{"sourceConfigEnabled": false}'::jsonb
from organization
on conflict (org_id, scope) do nothing;
