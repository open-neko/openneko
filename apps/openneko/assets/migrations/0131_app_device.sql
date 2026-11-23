-- Device sign-in for the installed app. A person approves a device in a
-- signed-in browser; the device then holds a bearer token. Only a SHA-256
-- digest of each secret is stored.

create table if not exists app_device_request (
  id uuid primary key default gen_random_uuid(),
  org_id text not null references organization(id) on delete cascade,
  device_code_verifier text not null unique,
  user_code text not null unique,
  name text not null,
  approved_by_user_id text references app_user(id) on delete cascade,
  approved_at timestamptz,
  denied_at timestamptz,
  last_polled_at timestamptz,
  expires_at timestamptz not null,
  created_at timestamptz not null default now()
);

create index if not exists app_device_request_org_idx
  on app_device_request (org_id, expires_at);

create table if not exists app_device (
  id uuid primary key default gen_random_uuid(),
  org_id text not null references organization(id) on delete cascade,
  user_id text not null references app_user(id) on delete cascade,
  name text not null,
  token_prefix text not null unique
    check (token_prefix ~ '^onk_dev_[0-9a-f]{12}$'),
  token_verifier text not null,
  created_at timestamptz not null default now(),
  last_used_at timestamptz not null default now(),
  revoked_at timestamptz,
  revoked_by_user_id text references app_user(id) on delete set null
);

create index if not exists app_device_org_idx on app_device (org_id, created_at desc);
create index if not exists app_device_user_idx on app_device (user_id);
