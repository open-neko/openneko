-- Web Push subscriptions for operator devices.

create table if not exists app_push_subscription (
  id uuid primary key default gen_random_uuid(),
  org_id text not null references organization(id) on delete cascade,
  user_id text not null references app_user(id) on delete cascade,
  device_id uuid references app_device(id) on delete cascade,
  endpoint text not null,
  p256dh text not null,
  auth text not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique (org_id, endpoint)
);

create index if not exists app_push_subscription_user_idx
  on app_push_subscription (org_id, user_id, created_at desc);
