export type BotSummary = {
  id: string;
  name: string;
  coat: string;
  pattern: "solid" | "patch" | "tabby";
  isDefault: boolean;
  threadId: string | null;
  instructions: string;
  lastMessageAt: string | null;
  /** A message arrived after the user last opened this chat. */
  unread: boolean;
  /** A run is in progress in this chat. */
  running: boolean;
  /** The latest run in this chat failed. */
  lastRunFailed: boolean;
};

export type BotRoutineRun = { id: string; status: string; at: string; summary: string };

export type BotRoutine = {
  id: string;
  name: string;
  schedule: string;
  enabled: boolean;
  description: string;
  steps: string[];
  runs: BotRoutineRun[];
  moreRuns: boolean;
};
