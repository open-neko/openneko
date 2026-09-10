import Link from "next/link";
import type { RecordObjectView } from "@neko/records";
import type { RecordOwnerIdentity } from "@/lib/records";
import type { PendingRecordAction } from "@/lib/records-pending";
import {
  recordReferenceIdentityKey,
  type RecordReferenceIdentity,
} from "@/lib/records-reference";
import { RecordCell } from "./RecordCell";
import { PendingChangeMarker } from "./PendingChangeMarker";
import { buttonClassName } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

function hrefWith(
  base: string,
  current: Record<string, string | undefined>,
  changes: Record<string, string | null>,
): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(current)) {
    if (value !== undefined) params.set(key, value);
  }
  for (const [key, value] of Object.entries(changes)) {
    if (value === null) params.delete(key);
    else params.set(key, value);
  }
  const query = params.toString();
  return query ? `${base}?${query}` : base;
}

export function RecordTable({
  appId,
  view,
  rows,
  owners,
  pendingActions = {},
  references = {},
  total,
  cursor,
  page,
  query,
}: {
  appId: string;
  view: RecordObjectView;
  rows: Array<Record<string, unknown>>;
  owners: Record<string, RecordOwnerIdentity>;
  pendingActions?: Record<string, PendingRecordAction[]>;
  references?: Record<string, RecordReferenceIdentity>;
  total: number;
  cursor: string | null;
  page: number;
  query: Record<string, string | undefined>;
}) {
  const base = `/a/${appId}/${view.object.apiName}`;
  const shownThrough = (page - 1) * 50 + rows.length;
  const hasMore = cursor !== null && shownThrough < total;
  return (
    <section
      className="records-list-card"
      aria-label={`${view.object.pluralLabel} records`}
    >
      <div className="records-table-scroll">
        <Table className="records-table">
          <TableHeader>
            <TableRow>
              {view.columns.map((column) => {
                const active = query.sort === column.apiName;
                const direction =
                  active && query.direction === "desc" ? "asc" : "desc";
                const sortable =
                  column.kind !== "owner" && column.kind !== "system_datetime";
                return (
                  <TableHead
                    key={column.apiName}
                    className={
                      ["integer", "decimal", "currency", "percent"].includes(
                        column.kind,
                      )
                        ? "is-number"
                        : undefined
                    }
                  >
                    {sortable ? (
                      <Link
                        href={hrefWith(base, query, {
                          sort: column.apiName,
                          direction,
                          after: null,
                          page: null,
                        })}
                      >
                        {column.label}
                        {active && (
                          <span aria-hidden="true">
                            {" "}
                            {query.direction === "desc" ? "↓" : "↑"}
                          </span>
                        )}
                      </Link>
                    ) : (
                      column.label
                    )}
                  </TableHead>
                );
              })}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row, rowIndex) => {
              const recordId = String(row.id ?? "");
              const recordPendingActions = pendingActions[recordId] ?? [];
              return (
                <TableRow
                  key={recordId || `row-${rowIndex}`}
                  className={
                    recordPendingActions.length > 0 ? "is-pending" : undefined
                  }
                >
                  {view.columns.map((column) => {
                    const value = row[column.columnName];
                    const ownerId =
                      column.kind === "owner" && typeof value === "string"
                        ? value
                        : null;
                    const nameColumn =
                      column.columnName === view.object.nameField;
                    const content = (
                      <RecordCell
                        appId={appId}
                        column={column}
                        value={value}
                        owner={ownerId ? owners[ownerId] : undefined}
                        reference={
                          references[
                            recordReferenceIdentityKey(
                              column.apiName,
                              String(value ?? ""),
                            )
                          ]
                        }
                        linkify={!nameColumn}
                      />
                    );
                    return (
                      <TableCell
                        key={column.apiName}
                        className={
                          [
                            "integer",
                            "decimal",
                            "currency",
                            "percent",
                          ].includes(column.kind)
                            ? "is-number"
                            : undefined
                        }
                      >
                        {nameColumn && recordId ? (
                          <span className="records-name-cell">
                            <Link
                              className="records-name-link"
                              href={`${base}/${encodeURIComponent(recordId)}`}
                            >
                              {content}
                            </Link>
                            <PendingChangeMarker
                              actions={recordPendingActions}
                            />
                          </span>
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
      {rows.length === 0 && (
        <div className="records-empty">
          <strong>No matching {view.object.pluralLabel.toLowerCase()}</strong>
          <span>Change the current search or create the first record.</span>
        </div>
      )}
      <footer className="records-list-foot">
        <span>
          {total === 0
            ? "No records"
            : `Showing ${(page - 1) * 50 + 1}–${shownThrough} of ${total}`}
        </span>
        {hasMore && (
          <Link
            className={buttonClassName({
              size: "sm",
              className: "records-page-button",
            })}
            href={hrefWith(base, query, {
              after: cursor,
              page: String(page + 1),
            })}
          >
            Next page <span aria-hidden="true">→</span>
          </Link>
        )}
      </footer>
    </section>
  );
}
