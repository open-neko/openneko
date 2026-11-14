import { Disclosure } from "@/components/ui/disclosure";

export type GraphjinRunState = {
  state: string;
  reason: string | null;
  since: string | null;
  restarts: number;
};

export type GraphjinConfigFileView = {
  path: string | null;
  yaml: string | null;
  error: string | null;
  state: GraphjinRunState | null;
};

function stateLine(state: GraphjinRunState | null): string {
  if (!state) return "OpenNeko cannot read GraphJin's state.";
  const label =
    state.state === "running"
      ? "GraphJin is running"
      : state.state === "restarting"
        ? "GraphJin is restarting"
        : state.state === "starting"
          ? "GraphJin is starting"
          : state.state === "stopped"
            ? "GraphJin is stopped"
            : `GraphJin state: ${state.state}`;
  return state.reason ? `${label}: ${state.reason}` : `${label}.`;
}

export function ConfigFileView({ view }: { view: GraphjinConfigFileView }) {
  const tone = view.state?.state === "running" ? "text-text2" : "text-danger";

  return (
    <section className="settings-card" aria-labelledby="config-file-title">
      <div className="settings-card-head">
        <div className="min-w-0">
          <h2 id="config-file-title" className="settings-card-title">
            Config file
          </h2>
          <p className="settings-card-copy">
            To change the config, edit this file on the server, then run{" "}
            <code className="font-mono">openneko graphjin check</code>.
          </p>
        </div>
      </div>
      <dl className="m-0 grid gap-2 text-ui-body-sm">
        <div className="min-w-0">
          <dt className="text-ui-caption text-text2">Path on the server</dt>
          <dd className="m-0 break-all font-mono text-text">{view.path ?? "Not set"}</dd>
        </div>
        <div className="min-w-0">
          <dt className="text-ui-caption text-text2">State</dt>
          <dd className={`m-0 break-words ${tone}`}>{stateLine(view.state)}</dd>
        </div>
      </dl>
      {view.error ? (
        <p className="mt-3 text-ui-body-sm text-danger">
          OpenNeko could not show the file: {view.error}.
        </p>
      ) : (
        <Disclosure className="mt-3" title="Show the file" meta="Read-only">
          <p className="m-0 text-ui-caption text-text2">
            OpenNeko hides secrets and comments.
          </p>
          <pre
            aria-label="GraphJin config, secrets hidden"
            className="mt-2 max-h-[480px] overflow-auto rounded-inner border border-border bg-bg px-3 py-3 font-mono text-xs leading-[1.6] text-text"
          >
            {view.yaml}
          </pre>
        </Disclosure>
      )}
    </section>
  );
}
