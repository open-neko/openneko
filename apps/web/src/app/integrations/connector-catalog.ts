/** Connectors OpenNeko ships as packs. Each shows in Connectors before an admin sets it up. */
export const SUPPORTED_CONNECTORS = [
  {
    packId: "google-workspace",
    label: "Google Workspace",
    description: "Connect Gmail, Drive, Calendar, Sheets, Docs, and Slides for your work and automations.",
  },
] as const;

export const COMING_SOON_CONNECTORS = [
  { label: "Microsoft Teams", description: "Chats, channels, and meetings." },
  { label: "Atlassian", description: "Jira issues and Confluence pages." },
] as const;
