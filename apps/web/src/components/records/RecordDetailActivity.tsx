import Link from "next/link";
import type { RecordObjectView } from "@neko/records";
import type { RecordChangeLogEntry, RecordRelatedList } from "@/lib/records";
import { RecordCell } from "./RecordCell";
import { recordReferenceIdentityKey } from "@/lib/records-reference";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

function display(value: unknown): string {
  if (value === null || value === undefined || value === "") return "Not set";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean")
    return String(value);
  return JSON.stringify(value);
}

export function RecordRelatedLists({
  appId,
  lists,
}: {
  appId: string;
  lists: RecordRelatedList[];
}) {
  if (lists.length === 0) return null;
  return (
    <section className="records-related" aria-label="Related records">
      {lists.map((list) => {
        const columns = list.view.columns.slice(0, 5);
        const base = `/a/${appId}/${list.view.object.apiName}`;
        return (
          <article key={`${list.view.object.apiName}-${list.fieldApiName}`}>
            <header>
              <h2>{list.label}</h2>
              <Link href={base}>View all</Link>
            </header>
            {list.rows.length === 0 ? (
              <p>No related {list.view.object.pluralLabel.toLowerCase()}.</p>
            ) : (
              <div className="records-related-scroll">
                <Table>
                  <TableHeader>
                    <TableRow>
                      {columns.map((column) => (
                        <TableHead key={column.apiName}>
                          {column.label}
                        </TableHead>
                      ))}
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {list.rows.map((row, index) => {
                      const id = String(row.id ?? "");
                      return (
                        <TableRow key={id || index}>
                          {columns.map((column) => (
                            <TableCell key={column.apiName}>
                              {column.columnName ===
                                list.view.object.nameField && id ? (
                                <Link
                                  href={`${base}/${encodeURIComponent(id)}`}
                                >
                                  <RecordCell
                                    appId={appId}
                                    column={column}
                                    value={row[column.columnName]}
                                    linkify={false}
                                  />
                                </Link>
                              ) : (
                                <RecordCell
                                  appId={appId}
                                  column={column}
                                  value={row[column.columnName]}
                                  owner={
                                    column.kind === "owner" &&
                                    typeof row[column.columnName] === "string"
                                      ? list.owners[
                                          String(row[column.columnName])
                                        ]
                                      : undefined
                                  }
                                  reference={
                                    list.references[
                                      recordReferenceIdentityKey(
                                        column.apiName,
                                        String(row[column.columnName] ?? ""),
                                      )
                                    ]
                                  }
                                />
                              )}
                            </TableCell>
                          ))}
                        </TableRow>
                      );
                    })}
                  </TableBody>
                </Table>
              </div>
            )}
          </article>
        );
      })}
    </section>
  );
}

export function RecordChangeTimeline({
  history,
  view,
}: {
  history: RecordChangeLogEntry[];
  view: RecordObjectView;
}) {
  const labels = new Map(
    view.columns.map((column) => [column.apiName, column.label]),
  );
  return (
    <section className="records-history" aria-label="Change history">
      <header>
        <h2>Change history</h2>
        <span>{history.length} recent events</span>
      </header>
      {history.length === 0 ? (
        <p>No recorded changes yet.</p>
      ) : (
        <ol>
          {history.map((entry) => (
            <li key={entry.id}>
              <div className="records-history-event">
                <strong>{entry.action}</strong>
                <time dateTime={entry.at}>
                  {new Intl.DateTimeFormat("en", {
                    dateStyle: "medium",
                    timeStyle: "short",
                  }).format(new Date(entry.at))}
                </time>
                <span>{entry.actorUserId ?? "System"}</span>
              </div>
              {Object.entries(entry.changes).length > 0 && (
                <dl>
                  {Object.entries(entry.changes).map(([field, raw]) => {
                    const change =
                      raw && typeof raw === "object" && !Array.isArray(raw)
                        ? (raw as Record<string, unknown>)
                        : {};
                    return (
                      <div key={field}>
                        <dt>{labels.get(field) ?? field}</dt>
                        <dd>
                          <s>{display(change.old ?? change.from)}</s>
                          <span aria-hidden="true">→</span>
                          <ins>{display(change.new ?? change.to)}</ins>
                        </dd>
                      </div>
                    );
                  })}
                </dl>
              )}
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
