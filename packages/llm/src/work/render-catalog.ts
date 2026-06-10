/**
 * ST1 — the A2UI/BriefingCard catalog lives on the `render_cards` TOOL
 * description, not in the agent's base prompt. The channel that supplies
 * the tool supplies the catalog (web today; other surfaces ship their own
 * renderer), so the base prompt stays channel-neutral. Both backends use
 * this text: claude via the neko_ui SDK MCP server, hermes via the stdio
 * render stub's tools/list.
 */
export const RENDER_CARDS_DESCRIPTION = [
  "Render structured Neko cards inline in the chat using the A2UI v0.9 protocol.",
  "Prefer this over markdown when you have numeric findings, KPIs, or trends.",
  "",
  "Pass an ARRAY of A2UI v0.9 protocol messages — each must have",
  '`version: "v0.9"` plus exactly one of: `createSurface`, `updateComponents`,',
  "`updateDataModel`, `deleteSurface`. Bare component objects (e.g.",
  '`{ "type": "kpi_group", ... }`) are rejected — they MUST be wrapped in',
  "an `updateComponents` envelope.",
  "",
  "Catalog (catalogId `urn:app:catalog:briefing:v1`):",
  "  - `Markdown` — prose block. Use for ANY narrative text — your response",
  "    prose lives here, never outside the tool call. Props: text (markdown",
  "    string; supports headings, lists, tables, code blocks).",
  "  - `Briefing` — root container. Props: greeting, subtitle, role, children[].",
  "  - `BriefingCard` — KPI card with optional chart. Required props:",
  "    metricId (any string for ad-hoc cards, e.g. 'chat-1'),",
  '    source ("chat" for ad-hoc rendering),',
  '    mood ("good" | "watch" | "act"),',
  "    text (1-sentence headline), metric (e.g. '$498,376'),",
  "    label (e.g. 'Total Profit'), detail (1-3 sentences),",
  '    chartType ("kpi" | "line" | "bar" | "area" | "donut"),',
  "    chartData (array of `{ d: string, v: number, t?: number }`,",
  '    or `[]` when chartType="kpi").',
  "",
  "Typical message sequence:",
  "  1. createSurface (once)",
  "  2. updateComponents with a `Briefing` root + 1-N `BriefingCard` children",
  "",
  "Example (one KPI card, no chart):",
  "[",
  '  { "version":"v0.9", "createSurface":{ "surfaceId":"s1",',
  '    "catalogId":"urn:app:catalog:briefing:v1" } },',
  '  { "version":"v0.9", "updateComponents":{ "surfaceId":"s1", "components":[',
  '    { "id":"root", "component":"Briefing", "greeting":"Top product",',
  '      "subtitle":"All stores", "role":"CEO", "children":["c1"] },',
  '    { "id":"c1", "component":"BriefingCard", "metricId":"chat-1",',
  '      "source":"chat", "mood":"good", "text":"Mountain-200 Silver leads",',
  '      "metric":"$498,376", "label":"Total Profit",',
  '      "detail":"$3.19M revenue − $2.70M cost on 2,130 units sold.",',
  '      "chartType":"kpi", "chartData":[] }',
  "  ]}}",
  "]",
].join("\n");
