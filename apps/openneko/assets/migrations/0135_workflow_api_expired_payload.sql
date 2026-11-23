-- An expired admission keeps no request payload, input file or batch contract.
alter table workflow_api_admission drop constraint if exists workflow_api_admission_payload_location;
alter table workflow_api_admission add constraint workflow_api_admission_payload_location check (
  (status = 'expired' and request_payload is null and input_file_path is null and batch_contract is null) or
  (execution_mode = 'single' and request_payload is not null and input_file_path is null and batch_contract is null and accepted_records is null) or
  (execution_mode = 'batch' and request_payload is null and input_file_path is not null and batch_contract is not null and accepted_records between 1 and 10000)
);
