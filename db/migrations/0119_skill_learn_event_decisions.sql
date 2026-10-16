-- Skill discovery records created and extended decisions, and the learner
-- records rolled_back. The original check allowed none of them.

alter table skill_learn_event
  drop constraint if exists skill_learn_event_decision_check;

alter table skill_learn_event
  add constraint skill_learn_event_decision_check
  check (decision in ('applied', 'skipped', 'stale', 'rejected', 'rolled_back', 'created', 'extended'));
