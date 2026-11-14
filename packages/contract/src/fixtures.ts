export const FIXTURE = {
  adminUserId: "contract-admin",
  memberUserId: "contract-member",
  adminWorkflowId: "c0a7ac70-0000-4000-8000-000000000001",
  memberWorkflowId: "c0a7ac70-0000-4000-8000-000000000002",
  adminThreadId: "c0a7ac70-0000-4000-8000-000000000011",
  memberThreadId: "c0a7ac70-0000-4000-8000-000000000012",
  adminWorkRunId: "c0a7ac70-0000-4000-8000-000000000021",
  memberWorkRunId: "c0a7ac70-0000-4000-8000-000000000022",
  serviceThreadId: "c0a7ac70-0000-4000-8000-000000000013",
  serviceWorkRunId: "c0a7ac70-0000-4000-8000-000000000023",
  adminWorkflowRunId: "c0a7ac70-0000-4000-8000-000000000031",
  memberWorkflowRunId: "c0a7ac70-0000-4000-8000-000000000032",
  adminOutputId: "c0a7ac70-0000-4000-8000-000000000041",
  memberOutputId: "c0a7ac70-0000-4000-8000-000000000042",
  adminActionRequestId: "c0a7ac70-0000-4000-8000-000000000051",
  memberActionRequestId: "c0a7ac70-0000-4000-8000-000000000052",
  unknownId: "c0a7ac70-0000-4000-8000-00000000dead",
} as const;

export type Fixture = typeof FIXTURE;
