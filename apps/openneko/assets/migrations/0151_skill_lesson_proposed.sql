-- A learned lesson waits for an admin as a proposal.
alter table skill_learn_event drop constraint if exists skill_learn_event_decision_check;
alter table skill_learn_event add constraint skill_learn_event_decision_check check (
  decision in ('applied', 'proposed', 'skipped', 'stale', 'rejected')
);
