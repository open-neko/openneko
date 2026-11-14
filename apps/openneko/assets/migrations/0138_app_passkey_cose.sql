-- Passkey public keys move to the COSE form that go-webauthn stores, and
-- each passkey keeps its backup-eligible flag. Null means not yet known.

alter table app_passkey add column if not exists backup_eligible boolean;

update app_passkey
set public_key = rtrim(translate(replace(encode(
      '\xa5010203262001215820'::bytea || substring(legacy.k from 2 for 32)
        || '\x225820'::bytea || substring(legacy.k from 34 for 32),
      'base64'), E'\n', ''), '+/', '-_'), '=')
from (
  select id, decode(rpad(translate(public_key, '-_', '+/'), 88, '='), 'base64') as k
  from app_passkey
  where length(public_key) = 87
) legacy
where app_passkey.id = legacy.id
  and length(legacy.k) = 65
  and get_byte(legacy.k, 0) = 4;
