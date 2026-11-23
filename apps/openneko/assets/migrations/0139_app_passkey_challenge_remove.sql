-- Removal of a passkey asks for a passkey assertion, so a challenge can
-- carry the purpose 'remove'.

alter table app_passkey_challenge drop constraint if exists app_passkey_challenge_purpose_check;
alter table app_passkey_challenge add constraint app_passkey_challenge_purpose_check
  check (purpose in ('register', 'approve', 'remove'));
