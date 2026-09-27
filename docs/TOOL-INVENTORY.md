# OpenNeko tool inventory for Harness

Source snapshot: `packages/llm/src/work/agent-core.ts`, `work/tools.ts`,
`work/interaction-server.ts`, `workflows/{builder,rule-builder,action,output}-server.ts`
on `feat/openneko-harness`, 2026-09-27. Names below are logical MCP
`server.tool` names; the Go catalog uses pinned aliases such as
`mcp_neko_records_find_records`. A discovered MCP tool is never admitted by
discovery alone. “Excluded” means the Harness bridge and broker deny it, while
Hermes can retain its existing path.

| Surface | Logical tools / capability | Harness status and recovery boundary |
| --- | --- | --- |
| Business data | GraphJin server agent and its catalog/query tools | `lookup` delegates to the host GraphJin agent; each call is host-journaled. Direct GraphJin MCP calls are excluded. |
| Records | `neko_records.browse_catalog`, `browse_blueprints`, `find_records`, `get_record`, `find_recycled_records`, `get_recycled_record` | Admitted read-only for actor-bound Work runs. Empty and populated registry paths passed real GraphJin, MCP, OpenShell and Ax. Results are checkpointed by the Go run; no write authority. |
| Knowledge | `neko_memory.search`, `neko_library.search` | Admitted for customer Work runs. Real memory and pgvector/entitlement fixtures passed. `neko_memory.save` is excluded pending an effect receipt and replay contract. |
| Interaction | `neko_interaction.ask_user_question`, `neko_ui.render_cards` | Admitted for eligible Work turns. Question pause/answer and validated card events passed connected worker/OpenShell checks. Browser reload remains open. |
| Skills and local files | `neko_skills.create_skill`; Harness `skill_read`, `file_read`, `file_edit`, `file_write`, `file_search`, `upload_read`, `upload_search` | Skill creation is excluded. Read-only staged skill and upload tools and run-artifact file tools passed connected turns; Go file writes require a matching read version or create-only target. |
| Workflow definition | `neko_workflow_builder.create_workflow`, `list_workflows`, `delete_workflow`; `neko_rule_builder.save_rule`, `list_rules` | Excluded from Harness until host write identity, authorization and crash recovery are qualified. The separately versioned workflow batch executor is a queued API/workflow path, not a skill or model tool. |
| Workflow execution | `neko_action.request`, `neko_workflow_output.emit` | Excluded from Harness agent turns pending a trusted workflow/run binding and durable receipt. |
| Integration action | Dynamic `neko_plugin_actions.*`, `neko_pack_actions.*` | Eligible pack kinds only can enter the `propose` approval path; effect execution uses OpenNeko's M4 claim/reconcile boundary. Plugin auto execution is excluded. |
| Administration | `neko_plugin_manager.list_plugins`, `request_plugin_install`, `request_plugin_uninstall`; `neko_user_manager.list_users`, `request_user_change`, `list_groups`, `request_group_change`, `request_item_grant`, `request_data_access_change`; `neko_channel_manager.list_channels`, `request_channel_change`; `neko_data_source_manager.list_data_sources`, `request_data_source_change`; `neko_source_config_manager.describe_source_graph`, `list_source_secret_names`, `ask_graphjin_config_agent`, `import_openapi_spec`, `list_openapi_specs`, `request_source_config_change`; `neko_audit.audit_trail` | Excluded from Harness until actor-specific read eligibility and approval/effect contracts are tested. No admin rights are inferred from MCP names or descriptions. |
| Delegation | Hermes `delegate_task`; Ax child agents | Excluded from Harness until narrowed child admission, shared budgets, cancellation and recovery pass. |

The adapter's actual admitted set lives in
`adapters/openneko/cmd/harness/main.go` and `adapters/openneko/mcp/memory.go`.
Go `Tools.admitted` validates schema and effect metadata, sorts the catalog,
hashes its binding into the checkpoint, and builds the Ax prompt from the same
admitted list. `adapters/mcp.Admit` rejects unlisted names, schema drift and
unqualified mutating effects. The host broker is a separate authorization
boundary: a forged org/run in tool arguments cannot change the token binding.
