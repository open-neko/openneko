-- The GraphJin data-agent opt-in is gone. Agent runs use the direct data tools.
delete from llm_provider_config where scope = 'graphjin-agent';

alter table llm_provider_config
  drop constraint if exists llm_provider_config_scope_check;

alter table llm_provider_config
  add constraint llm_provider_config_scope_check
  check (scope in ('primary', 'research', 'agent', 'install-policy', 'graphjin-config', 'public-url'));
