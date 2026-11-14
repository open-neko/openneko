-- Passkeys (WebAuthn public-key credentials) for approving actions.

create table if not exists app_passkey (
  id uuid primary key default gen_random_uuid(),
  org_id text not null references organization(id) on delete cascade,
  user_id text not null references app_user(id) on delete cascade,
  credential_id text not null,
  public_key text not null,
  algorithm integer not null default -7,
  sign_count bigint not null default 0,
  name text not null,
  created_at timestamptz not null default now(),
  last_used_at timestamptz,
  unique (org_id, credential_id)
);

create index if not exists app_passkey_user_idx
  on app_passkey (org_id, user_id, created_at desc);

create table if not exists app_passkey_challenge (
  id uuid primary key default gen_random_uuid(),
  org_id text not null references organization(id) on delete cascade,
  user_id text not null references app_user(id) on delete cascade,
  purpose text not null check (purpose in ('register', 'approve')),
  action_request_id uuid references action_request(id) on delete cascade,
  challenge text not null unique,
  expires_at timestamptz not null,
  created_at timestamptz not null default now()
);

create index if not exists app_passkey_challenge_user_idx
  on app_passkey_challenge (org_id, user_id, expires_at desc);
