import { registerActionAdapter } from "../action-executor";
import { webhookAdapter } from "./webhook";

let registered = false;

/**
 * Register every built-in action adapter with the executor registry.
 * Idempotent; safe to call multiple times. Tests can call this to bring
 * the same adapters into scope.
 */
export function registerBuiltinAdapters(): void {
  if (registered) return;
  registered = true;
  registerActionAdapter("send_webhook", webhookAdapter);
}

export { webhookAdapter, WebhookAdapterError } from "./webhook";
