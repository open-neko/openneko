export function terminalHint(available: boolean, blocked?: string): string {
  if (!available) {
    return "Unavailable: this install has no sandbox service. Set OPENNEKO_SANDBOX_URL on the server to use it.";
  }
  if (blocked) return blocked;
  return "Lets the agent run commands, script skills and workflow script steps in the sandbox service. The sandbox gets no model key and no database login. Applies from the next run.";
}
