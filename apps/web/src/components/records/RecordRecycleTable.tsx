import Link from "next/link";
import type { RecycledRecordSummary } from "@neko/records";
import type { RecordOwnerIdentity } from "@/lib/records";
import { buttonClassName } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

function dateLabel(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? "—"
    : new Intl.DateTimeFormat("en", {
        dateStyle: "medium",
        timeStyle: "short",
      }).format(date);
}

function hrefWith(
  base: string,
  current: Record<string, string | undefined>,
  changes: Record<string, string | null>,
): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(current)) {
    if (value) params.set(key, value);
  }
  for (const [key, value] of Object.entries(changes)) {
    if (value === null) params.delete(key);
    else params.set(key, value);
  }
  const query = params.toString();
  return query ? `${base}?${query}` : base;
}

export function RecordRecycleTable({
  appId,
  objectApiName,
  objectPluralLabel,
  rows,
  owners,
  total,
  cursor,
  page,
  query,
}: {
  appId: string;
  objectApiName: string;
  objectPluralLabel: string;
  rows: RecycledRecordSummary[];
  owners: Record<string, RecordOwnerIdentity>;
  total: number;
  cursor: string | null;
  page: number;
  query: Record<string, string | undefined>;
}) {
  const base = `/a/${appId}/${objectApiName}/recycle`;
  const shownThrough = (page - 1) * 50 + rows.length;
  const hasMore = cursor !== null && shownThrough < total;
  return (
    <section
      className="records-list-card"
      aria-label={`${objectPluralLabel} recycle bin`}
    >
      <div className="records-table-scroll">
        <Table className="records-table records-recycle-table">
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Deleted</TableHead>
              <TableHead>Deleted by</TableHead>
              <TableHead>Owner</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => {
              const owner = row.ownerUserId
                ? owners[row.ownerUserId]
                : undefined;
              return (
                <TableRow key={row.recordId}>
                  <TableCell>
                    <Link
                      className="records-name-link"
                      href={`${base}/${encodeURIComponent(row.recordId)}`}
                    >
                      {row.recordName}
                    </Link>
                    <small className="records-recycle-id">{row.recordId}</small>
                  </TableCell>
                  <TableCell>
                    <time dateTime={row.deletedAt}>
                      {dateLabel(row.deletedAt)}
                    </time>
                  </TableCell>
                  <TableCell>{row.deletedBy ?? "—"}</TableCell>
                  <TableCell>
                    {owner?.label ?? row.ownerUserId ?? "—"}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </div>
      {rows.length === 0 && (
        <div className="records-empty">
          <strong>Recycle bin is empty</strong>
          <span>
            Deleted {objectPluralLabel.toLowerCase()} will appear here.
          </span>
        </div>
      )}
      <footer className="records-list-foot">
        <span>
          {total === 0
            ? "No deleted records"
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
