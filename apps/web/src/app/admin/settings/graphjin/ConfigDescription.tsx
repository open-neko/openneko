import { readSourceConfigView } from "@neko/llm/work";
import { readConfigSentences } from "@/lib/config-summary";

const KIND_LABELS: Record<string, string> = {
  database: "Database",
  api: "API",
  file: "Files",
};

export type DescribedSource = { id: string; name: string };

async function describeOne(orgId: string, userId: string | null, source: DescribedSource) {
  const view = await readSourceConfigView({
    orgId,
    userId,
    dataSourceId: source.id,
    signal: AbortSignal.timeout(8_000),
  });
  return {
    source,
    denied: Boolean(view.denied),
    error: view.error ?? null,
    summary: view.summary ? readConfigSentences(view.summary) : null,
  };
}

export async function ConfigDescription({
  orgId,
  userId,
  sources,
}: {
  orgId: string;
  userId: string | null;
  sources: DescribedSource[];
}) {
  const results = (
    await Promise.all(sources.map((source) => describeOne(orgId, userId, source)))
  ).filter((result) => !result.denied);

  return (
    <section className="settings-card" aria-labelledby="config-description-title">
      <div className="settings-card-head">
        <div>
          <h2 id="config-description-title" className="settings-card-title">
            What the config allows
          </h2>
          <p className="settings-card-copy">
            Each source GraphJin serves, what it exposes and who may read or change it.
          </p>
        </div>
      </div>
      {results.length === 0 ? (
        <p className="text-ui-body-sm text-text2">No customer data source is connected yet.</p>
      ) : (
        <div className="grid gap-4">
          {results.map(({ source, error, summary }) => (
            <div key={source.id} className="grid gap-3">
              {results.length > 1 ? (
                <h3 className="m-0 text-ui-body-sm font-semibold text-text">{source.name}</h3>
              ) : null}
              {error ? (
                <p className="m-0 text-ui-body-sm text-danger">
                  OpenNeko could not read this config: {error}
                </p>
              ) : null}
              {summary && summary.sources.length === 0 && !error ? (
                <p className="m-0 text-ui-body-sm text-text2">GraphJin has no source set up yet.</p>
              ) : null}
              {summary?.sources.map((item) => (
                <div
                  key={item.name}
                  className="overflow-hidden rounded-inner border border-border bg-card"
                >
                  <h4 className="m-0 border-b border-border px-3.5 py-2 font-display text-ui-caption font-semibold text-text2">
                    {KIND_LABELS[item.kind] ?? "Source"} · {item.name}
                  </h4>
                  <ul className="m-0 grid list-none gap-1 px-3.5 py-3 text-ui-body-sm leading-[1.6] text-text">
                    {item.sentences.map((sentence, index) => (
                      <li key={index} className="break-words">
                        {sentence}
                      </li>
                    ))}
                  </ul>
                </div>
              ))}
              {summary && summary.roles.length > 0 ? (
                <div className="overflow-hidden rounded-inner border border-border bg-card">
                  <h4 className="m-0 border-b border-border px-3.5 py-2 font-display text-ui-caption font-semibold text-text2">
                    Roles
                  </h4>
                  <ul className="m-0 grid list-none gap-1 px-3.5 py-3 text-ui-body-sm leading-[1.6] text-text">
                    {summary.roles.map((role) => (
                      <li key={role.name} className="break-words">
                        <span className="font-semibold">{role.name}</span>: {role.sentence}
                      </li>
                    ))}
                  </ul>
                </div>
              ) : null}
            </div>
          ))}
        </div>
      )}
    </section>
  );
}
