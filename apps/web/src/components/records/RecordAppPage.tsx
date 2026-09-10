import Link from "next/link";
import type {
  RecordAppPageMetricBlock,
  RecordAppPageResult,
  RecordAppPageRowsBlock,
} from "@/lib/records";
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

function metricValue(block: RecordAppPageMetricBlock): string {
  const numeric = Number(block.value);
  if (!Number.isFinite(numeric)) return String(block.value ?? "—");
  const scale = Math.min(Math.max(block.field?.scale ?? 0, 0), 6);
  return new Intl.NumberFormat("en", {
    ...(block.field?.kind === "currency"
      ? { style: "currency" as const, currency: "USD" }
      : {}),
    maximumFractionDigits: scale,
    minimumFractionDigits:
      block.field?.kind === "currency" ? Math.min(scale, 2) : 0,
  }).format(numeric);
}

function RowsBlock({
  appId,
  block,
}: {
  appId: string;
  block: RecordAppPageRowsBlock;
}) {
  const columns = block.view.columns.slice(
    0,
    block.renderer === "timeline" ? 3 : 5,
  );
  const base = `/a/${appId}/${block.view.object.apiName}`;
  if (block.rows.length === 0) {
    return (
      <p className="records-page-empty">
        No matching {block.view.object.pluralLabel.toLowerCase()}.
      </p>
    );
  }
  if (block.renderer === "timeline") {
    return (
      <ol className="records-page-timeline">
        {block.rows.map((row, index) => {
          const id = String(row.id ?? "");
          const name = String(
            row[block.view.object.nameField] ?? (id || "Record"),
          );
          return (
            <li key={id || index}>
              <Link href={`${base}/${encodeURIComponent(id)}`}>{name}</Link>
              <div>
                {columns
                  .filter(
                    (column) =>
                      column.columnName !== block.view.object.nameField,
                  )
                  .map((column) => (
                    <span key={column.apiName}>
                      <b>{column.label}</b>{" "}
                      <RecordCell
                        appId={appId}
                        column={column}
                        value={row[column.columnName]}
                        owner={
                          column.kind === "owner" &&
                          typeof row[column.columnName] === "string"
                            ? block.owners[String(row[column.columnName])]
                            : undefined
                        }
                        reference={
                          block.references[
                            recordReferenceIdentityKey(
                              column.apiName,
                              String(row[column.columnName] ?? ""),
                            )
                          ]
                        }
                      />
                    </span>
                  ))}
              </div>
            </li>
          );
        })}
      </ol>
    );
  }
  return (
    <div className="records-page-table-wrap">
      <Table className="records-page-table">
        <TableHeader>
          <TableRow>
            {columns.map((column) => (
              <TableHead key={column.apiName}>{column.label}</TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {block.rows.map((row, index) => {
            const id = String(row.id ?? "");
            return (
              <TableRow key={id || index}>
                {columns.map((column) => {
                  const content = (
                    <RecordCell
                      appId={appId}
                      column={column}
                      value={row[column.columnName]}
                      owner={
                        column.kind === "owner" &&
                        typeof row[column.columnName] === "string"
                          ? block.owners[String(row[column.columnName])]
                          : undefined
                      }
                      reference={
                        block.references[
                          recordReferenceIdentityKey(
                            column.apiName,
                            String(row[column.columnName] ?? ""),
                          )
                        ]
                      }
                      linkify={
                        column.columnName !== block.view.object.nameField
                      }
                    />
                  );
                  return (
                    <TableCell key={column.apiName}>
                      {column.columnName === block.view.object.nameField &&
                      id ? (
                        <Link href={`${base}/${encodeURIComponent(id)}`}>
                          {content}
                        </Link>
                      ) : (
                        content
                      )}
                    </TableCell>
                  );
                })}
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}

export function RecordAppPage({ result }: { result: RecordAppPageResult }) {
  return (
    <section className="records-page-grid" aria-label={result.page.label}>
      {result.blocks.map((block, index) => (
        <article
          className={`records-page-block is-span-${block.span} is-${block.renderer}`}
          key={`${block.label}-${index}`}
        >
          <header>
            <h2>{block.label}</h2>
            {block.renderer !== "metric" && (
              <Link
                href={`/a/${result.app.appId}/${block.view.object.apiName}`}
              >
                View all
              </Link>
            )}
          </header>
          {block.renderer === "metric" ? (
            <strong className="records-page-metric">
              {metricValue(block)}
            </strong>
          ) : (
            <RowsBlock appId={result.app.appId} block={block} />
          )}
        </article>
      ))}
    </section>
  );
}
