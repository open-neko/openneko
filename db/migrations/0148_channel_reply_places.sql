-- New installs answer a channel mention privately unless an admin lists the
-- channel. Organizations that exist now keep answering in the thread.
insert into llm_provider_config (org_id, scope, provider, enabled, config)
select id, 'channel-replies', 'channel-replies', true,
  '{"slack": {"mode": "thread", "places": []}, "telegram": {"mode": "thread", "places": []}, "keptFromUpgrade": true}'::jsonb
from organization
on conflict (org_id, scope) do nothing;
