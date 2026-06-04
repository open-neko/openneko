"use client";

import { createContext, useContext, useMemo, useState } from "react";

export type RailArtifact = { path: string; label: string; mimeType?: string };

type WorkShellContextValue = {
  activeRunId: string | null;
  setActiveRunId: (runId: string | null) => void;
  // Artifacts produced by the active thread's runs, lifted from WorkScreen so
  // the context rail (rendered up in the shell layout) can list them.
  railArtifacts: RailArtifact[];
  setRailArtifacts: (a: RailArtifact[]) => void;
};

const WorkShellContext = createContext<WorkShellContextValue>({
  activeRunId: null,
  setActiveRunId: () => {},
  railArtifacts: [],
  setRailArtifacts: () => {},
});

export function WorkShellProvider({ children }: { children: React.ReactNode }) {
  const [activeRunId, setActiveRunId] = useState<string | null>(null);
  const [railArtifacts, setRailArtifacts] = useState<RailArtifact[]>([]);
  const value = useMemo(
    () => ({ activeRunId, setActiveRunId, railArtifacts, setRailArtifacts }),
    [activeRunId, railArtifacts],
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
