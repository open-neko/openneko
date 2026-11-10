-- Records data lived in the separate records database; nothing here moves it.

delete from workflow_definition
where owner_user_id = ''
  and name in (
    'CRM · Opportunities with no activity',
    'CRM · Unlinked or departed owners',
    'CRM · Deals closing this month',
    'OpenNeko operations'
  );

drop table if exists records_watch_event_receipt;
drop table if exists records_watch_binding;
drop table if exists records_ops_health;
drop table if exists app_state;

delete from action_policy
where name in (
  'record_mutation_default',
  'record_schema_default',
  'record_access_default',
  'record_backfill_default',
  'records_import_mutation_default',
  'records_import_status_default',
  'records_identity_backfill_default',
  'records_identity_backfill_lazy_default',
  'records_identity_backfill_import_default',
  'records_salesforce_discovery_default',
  'records_salesforce_control_default',
  'records_salesforce_sync_default',
  'records_salesforce_cutover_default',
  'records_salesforce_status_default',
  'records_artifact_import_default',
  'records_artifact_import_from_export_default'
);

delete from item_grant
where item_type = 'action'
  and item_id in (
    'record_create', 'record_update', 'record_delete', 'record_restore',
    'record_backfill',
    'app_create', 'app_object_create', 'app_field_add', 'app_field_modify',
    'app_object_archive', 'app_field_archive', 'app_permission_set',
    'app_layout_update',
    'app_access_grant', 'app_access_revoke', 'app_object_permission_set',
    'app_field_permission_set',
    'records_import_start', 'records_import_cancel', 'records_import_status',
    'records_identity_backfill', 'records_identity_backfill_lazy',
    'records_identity_backfill_import',
    'records_salesforce_discover', 'records_salesforce_export_start',
    'records_salesforce_export_cancel', 'records_salesforce_export_status',
    'records_salesforce_sync_delta', 'records_salesforce_cutover',
    'records_artifact_import_start', 'records_artifact_import_from_export'
  );

update work_thread
set backend_state = backend_state - 'appContext' - 'recordContext'
where backend_state ?| array['appContext', 'recordContext'];
