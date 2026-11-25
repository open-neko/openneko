-- The Decisions task: yes-or-no and choice questions that a decision model
-- answers. With no route for it, the default route answers them. The
-- question classifier task is gone; classifiers use the Decisions task.
delete from llm_task_route where task = 'classify';
alter table llm_task_route drop constraint if exists llm_task_route_task_check;
alter table llm_task_route add constraint llm_task_route_task_check check (task in (
  'default', 'turn', 'briefing_summary', 'compact',
  'bot_instructions', 'bot_names', 'skill_lessons', 'research_mission', 'decide'
));
