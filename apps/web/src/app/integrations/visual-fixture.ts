import type { PackUserConnectionStatus } from "@neko/llm/graphjin/pack-user-connections";

// Provider metadata mirrors ../plugins; account state is synthetic and actions are disabled.
export const pluginConnectionsFixture = {
  workspace: [{ pluginId: "scalekit", pluginName: "scalekit", providerLabel: "Scalekit workspace", scopes: ["wks:read", "wks:write", "env:read", "env:write", "org:read", "org:write"], flow: "mcp-oauth", credentialScope: "deployment", connected: true, connectedAt: "2026-09-01T10:00:00.000Z" }],
  connectors: [{ pluginId: "google-workspace", pluginName: "@open-neko/connector-google-workspace", providerLabel: "Google Workspace", scopes: ["openid", "https://www.googleapis.com/auth/userinfo.email", "https://www.googleapis.com/auth/gmail.send", "https://www.googleapis.com/auth/calendar.events.readonly", "https://www.googleapis.com/auth/spreadsheets", "https://www.googleapis.com/auth/documents"], flow: "oauth2-pkce", credentialScope: "operator", connected: false, connectedAt: null }],
};

// Visual testing uses the bundled Google Workspace declaration; only account state is synthetic.
export function personalConnectionsFixture(state: string): PackUserConnectionStatus[] {
  return [
    {
      packId: "google-workspace",
      key: "workspace",
      providerLabel: "Google Workspace",
      experience: {
        description: "Connect Gmail, Drive, Calendar, Sheets, Docs, and Slides for your work and automations.",
        setupInstructions: "Create a Web application OAuth client in your Google Cloud project. Enable the APIs used by this pack and configure the consent screen. Add the redirect URI below, then save the client ID and client secret. Each user connects their own Google account on Integrations.",
        helpUrl: "https://developers.google.com/identity/protocols/oauth2/web-server",
      },
      scopes: [
        "openid",
        "https://www.googleapis.com/auth/userinfo.email",
        "https://www.googleapis.com/auth/gmail.modify",
        "https://www.googleapis.com/auth/gmail.compose",
        "https://www.googleapis.com/auth/drive",
        "https://www.googleapis.com/auth/calendar.readonly",
        "https://www.googleapis.com/auth/calendar.events",
        "https://www.googleapis.com/auth/spreadsheets",
        "https://www.googleapis.com/auth/documents.readonly",
        "https://www.googleapis.com/auth/presentations.readonly",
      ],
      configured: state !== "unconfigured",
      connected: state === "connected",
      accountLabel: state === "connected" ? "synthetic-user@example.test" : null,
    },
  ];
}
