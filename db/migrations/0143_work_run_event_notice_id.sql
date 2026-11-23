-- The notice names the event id; a joined text row keeps the ids and end
-- offsets of the rows it replaced.

create or replace function notify_work_run_event() returns trigger as $$
begin
  perform pg_notify('work_run_event', NEW.run_id::text || ':' || NEW.id::text);
  return NEW;
end;
$$ language plpgsql;

alter table work_run_event
  add column if not exists joined_ids bigint[],
  add column if not exists joined_ends integer[];
