import {
  mockActionAdapter,
  registerActionAdapter,
} from "../action-executor";
import { draftPatchAdapter, makeCreateIssueAdapter } from "./code";
import { webhookAdapter } from "./webhook";

let registered = false;

/**
 * Register every built-in action adapter with the executor registry.
 * Idempotent; safe to call multiple times. Tests can call this to bring
 * the same adapters into scope.
 *
 * Dry-run mode: when NEKO_ACTIONS_DRY_RUN=true, every external-side-effect
 * adapter (currently send_webhook) is swapped for `mockActionAdapter`,
 * which records the request as executed without firing the actual HTTP
 * call. Useful for local-dev smoke tests where you don't want approvals
 * to ping real Slack/webhook endpoints.
 */
export function registerBuiltinAdapters(): void {
  if (registered) return;
  registered = true;
  const dryRun = process.env.NEKO_ACTIONS_DRY_RUN === "true";
  // OL6: the patch draft is a local artifact (no external side effect),
  // so it stays real even in dry-run.
  registerActionAdapter("code_draft_patch", draftPatchAdapter);
  if (dryRun) {
    // eslint-disable-next-line no-console
    console.warn(
      "[actions] NEKO_ACTIONS_DRY_RUN=true — using mock adapter for all external action kinds. No real webhooks will fire.",
    );
    registerActionAdapter("send_webhook", mockActionAdapter);
    registerActionAdapter("code_create_issue", mockActionAdapter);
    return;
  }
  registerActionAdapter("send_webhook", webhookAdapter);
  registerActionAdapter("code_create_issue", makeCreateIssueAdapter());
}

export { webhookAdapter, WebhookAdapterError } from "./webhook";
export {
  CodeActionError,
  draftPatchAdapter,
  makeCreateIssueAdapter,
} from "./code";
