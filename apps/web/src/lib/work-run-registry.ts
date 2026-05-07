import "server-only";

const activeRuns = new Map<string, AbortController>();

export function registerWorkRun(runId: string, controller: AbortController) {
  activeRuns.set(runId, controller);
  return () => {
    activeRuns.delete(runId);
  };
}

export function cancelWorkRun(runId: string): boolean {
  const controller = activeRuns.get(runId);
  if (!controller) return false;
  controller.abort();
  activeRuns.delete(runId);
  return true;
}
