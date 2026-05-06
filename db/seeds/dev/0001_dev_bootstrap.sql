insert into organization (
  id,
  name,
  plan,
  status
)
values (
  :'dev_org_id',
  :'dev_org_name',
  'free',
  'active'
)
on conflict (id) do update
set
  name = excluded.name,
  updated_at = now();

insert into data_source (
  org_id,
  kind,
  graphql_url,
  mcp_url,
  label
)
select
  :'dev_org_id',
  'graphjin',
  :'customer_graphql_url',
  :'customer_mcp_url',
  'primary'
where not exists (
  select 1
  from data_source
  where org_id = :'dev_org_id'
);
