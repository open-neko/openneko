-- A question that the web app sends again after a lost response carries the
-- same client request id, so the server returns the run it made the first time.

alter table work_run add column if not exists client_request_id text;

create unique index if not exists work_run_client_request_uidx
  on work_run (thread_id, client_request_id)
  where client_request_id is not null;
