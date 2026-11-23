-- A question queued offline with no thread carries a client request id, so
-- the server returns the thread it made the first time.

alter table work_thread add column if not exists client_request_id text;

create unique index if not exists work_thread_client_request_uidx
  on work_thread (org_id, created_by_user_id, client_request_id)
  where client_request_id is not null;
