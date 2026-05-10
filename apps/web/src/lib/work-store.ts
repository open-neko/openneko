/**
 * Web shim — the canonical work_* table helpers now live in
 * @neko/llm/work so the worker can use them too. Existing web
 * imports of @/lib/work-store keep working through this re-export.
 *
 * The "server-only" guard stays here so any accidental client
 * import still errors at build time, even though the underlying
 * module no longer carries it (the worker is also server-side).
 */
import "server-only";

export {
  type WorkThreadSummary,
  type WorkMessageRecord,
  type WorkRunRecord,
  type WorkThreadBundle,
  listWorkThreads,
  createWorkThread,
  getWorkThread,
  setWorkThreadBackendState,
  touchWorkThread,
  createWorkRun,
  markWorkRunRunning,
  finishWorkRun,
  createWorkMessage,
  saveAssistantWorkMessage,
  appendWorkRunEvent,
  getWorkRunEvents,
  getWorkRunEventsAfter,
  getWorkRun,
  getWorkThreadBundle,
  suggestWorkThreadTitle,
} from "@neko/llm/work";
