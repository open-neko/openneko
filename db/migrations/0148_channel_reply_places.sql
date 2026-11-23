alter table llm_provider_config
  drop constraint if exists llm_provider_config_scope_check;

alter table llm_provider_config
  add constraint llm_provider_config_scope_check
  check (scope in ('primary', 'research', 'agent', 'install-policy', 'graphjin-config', 'public-url', 'channel-replies'));

-- New installs answer a channel mention privately unless an admin lists the
-- channel. Organizations that exist now keep answering in the thread.
insert into llm_provider_config (org_id, scope, provider, enabled, config)
select id, 'channel-replies', 'channel-replies', true,
  '{"slack": {"mode": "thread", "places": []}, "telegram": {"mode": "thread", "places": []}, "keptFromUpgrade": true}'::jsonb
from organization
on conflict (org_id, scope) do nothing;
