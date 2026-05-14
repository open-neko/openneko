"use client";

import { createContext, useContext, useMemo, useState } from "react";

type WorkShellContextValue = {
  activeRunId: string | null;
  setActiveRunId: (runId: string | null) => void;
};

const WorkShellContext = createContext<WorkShellContextValue>({
  activeRunId: null,
  setActiveRunId: () => {},
});

export function WorkShellProvider({ children }: { children: React.ReactNode }) {
  const [activeRunId, setActiveRunId] = useState<string | null>(null);
  const value = useMemo(
    () => ({ activeRunId, setActiveRunId }),
    [activeRunId],
  );
  return (
    <WorkShellContext.Provider value={value}>
      {children}
    </WorkShellContext.Provider>
  );
}

export function useWorkShell() {
  return useContext(WorkShellContext);
}
