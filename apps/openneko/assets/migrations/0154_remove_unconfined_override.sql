-- Agent turns run only in a confined runner. No setting turns that off.
-- work_run and processing_job keep ran_unconfined for the runs before 6.0.0.
delete from install_setting where name = 'agent_turns_unconfined';
drop table if exists install_setting;
