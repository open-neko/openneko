-- An agent job records that it ran without OS confinement, as a work run does.
alter table processing_job
  add column if not exists ran_unconfined boolean not null default false;
