-- A firing whose last queue attempt failed ends as 'failed' and is not
-- dispatched again.

alter table workflow_schedule_firing
  drop constraint if exists workflow_schedule_firing_status_check;
alter table workflow_schedule_firing
  add constraint workflow_schedule_firing_status_check
  check (status in ('pending', 'dispatching', 'enqueued', 'running', 'completed', 'cancelled', 'failed'));
