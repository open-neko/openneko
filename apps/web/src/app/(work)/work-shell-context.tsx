"use client";

import {
  createContext,
  useContext,
  useMemo,
  useRef,
  useState,
  type MutableRefObject,
} from "react";

export type RailArtifact = { path: string; label: string; mimeType?: string };
export type RailSource = { name: string; detail?: string };
export type RailVital = { label: string; value: string; sub?: string };

export type RailContext = {
  vitals: RailVital[];
  sources: RailSource[];
  followups: string[];
};

const EMPTY_RAIL: RailContext = { vitals: [], sources: [], followups: [] };

type WorkShellContextValue = {
  activeRunId: string | null;
  setActiveRunId: (runId: string | null) => void;
  // Artifacts produced by the active thread's runs, lifted from WorkScreen so
  // the context rail (rendered up in the shell layout) can list them.
  railArtifacts: RailArtifact[];
  setRailArtifacts: (a: RailArtifact[]) => void;
  // Right-rail context for the active thread: the answer's headline vitals and
  // follow-ups (channel-agnostic content the agent emits), plus sources touched
  // (derived from run telemetry). Lifted from WorkScreen.
  railContext: RailContext;
  setRailContext: (c: RailContext) => void;
  // Imperative handle WorkScreen registers so the rail can drop an "Ask next"
  // question into the CURRENT thread's composer (for the operator to edit),
  // without the rail reaching into WorkScreen's draft state.
  insertComposerRef: MutableRefObject<((q: string) => void) | null>;
};

const WorkShellContext = createContext<WorkShellContextValue>({
  activeRunId: null,
  setActiveRunId: () => {},
  railArtifacts: [],
  setRailArtifacts: () => {},
  railContext: EMPTY_RAIL,
  setRailContext: () => {},
  insertComposerRef: { current: null },
});

export function WorkShellProvider({ children }: { children: React.ReactNode }) {
  const [activeRunId, setActiveRunId] = useState<string | null>(null);
  const [railArtifacts, setRailArtifacts] = useState<RailArtifact[]>([]);
  const [railContext, setRailContext] = useState<RailContext>(EMPTY_RAIL);
  const insertComposerRef = useRef<((q: string) => void) | null>(null);
  const value = useMemo(
    () => ({
      activeRunId,
      setActiveRunId,
      railArtifacts,
      setRailArtifacts,
      railContext,
      setRailContext,
      insertComposerRef,
    }),
    [activeRunId, railArtifacts, railContext],
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
