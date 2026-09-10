"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import {
  Check,
  CircleAlert,
  Link2,
  RefreshCcw,
  UserRoundX,
} from "lucide-react";
import { useState, type FormEvent } from "react";
import type {
  RecordIdentityAdminMapping,
  RecordIdentityAdminModel,
} from "@/lib/records-identity";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import { Spinner } from "@/components/ui/spinner";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

type IdentityStatus = RecordIdentityAdminMapping["status"];

const FILTERS: Array<{ value: IdentityStatus | "all"; label: string }> = [
  { value: "all", label: "All" },
  { value: "conflict", label: "Conflicts" },
  { value: "unlinked", label: "Unlinked" },
  { value: "linked", label: "Linked" },
  { value: "ignored", label: "Ignored" },
];

function userLabel(user: RecordIdentityAdminModel["users"][number]): string {
  return user.name ? `${user.name} · ${user.email}` : user.email;
}

function sourceLabel(mapping: RecordIdentityAdminMapping): string {
  return mapping.sourceName || mapping.sourceEmail;
}

function StatusIcon({ status }: { status: IdentityStatus }) {
  if (status === "linked") return <Check aria-hidden="true" />;
  if (status === "conflict") return <CircleAlert aria-hidden="true" />;
  if (status === "ignored") return <UserRoundX aria-hidden="true" />;
  return <Link2 aria-hidden="true" />;
}

function MappingControl({
  appId,
  mapping,
  users,
}: {
  appId: string;
  mapping: RecordIdentityAdminMapping;
  users: RecordIdentityAdminModel["users"];
}) {
  const router = useRouter();
  const [appUserId, setAppUserId] = useState(mapping.appUserId ?? "");
  const [pending, setPending] = useState<"link" | "ignore" | null>(null);
  const [message, setMessage] = useState<string | null>(null);

  async function decide(decision: "link" | "ignore") {
    if (decision === "link" && !appUserId) {
      setMessage("Choose a user first.");
      return;
    }
    setPending(decision);
    setMessage(null);
    try {
      const response = await fetch(
        `/api/a/${encodeURIComponent(appId)}/identity/decision`,
        {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({
            sourceInstanceId: mapping.sourceInstanceId,
            sourceUserId: mapping.sourceUserId,
            decision,
            ...(decision === "link" ? { appUserId } : {}),
          }),
        },
      );
      const payload = (await response.json()) as { error?: string };
      if (!response.ok) {
        setMessage(payload.error ?? "The identity could not be updated.");
        return;
      }
      router.refresh();
    } catch {
      setMessage("The identity service could not be reached.");
    } finally {
      setPending(null);
    }
  }

  return (
    <form
      className="records-identity-control"
      onSubmit={(event: FormEvent<HTMLFormElement>) => {
        event.preventDefault();
        void decide("link");
      }}
    >
      <NativeSelect
        aria-label={`OpenNeko user for ${sourceLabel(mapping)}`}
        value={appUserId}
        onChange={(event) => setAppUserId(event.target.value)}
        disabled={pending !== null}
      >
        <option value="">Choose a user…</option>
        {users.map((user) => (
          <option key={user.id} value={user.id}>
            {userLabel(user)}
          </option>
        ))}
      </NativeSelect>
      <Button
        variant="primary"
        size="sm"
        type="submit"
        disabled={pending !== null || !appUserId}
      >
        {pending === "link" ? <Spinner className="records-spin" /> : <Link2 />}
        Link
      </Button>
      <Button
        size="sm"
        disabled={pending !== null}
        onClick={() => void decide("ignore")}
      >
        {pending === "ignore" ? (
          <Spinner className="records-spin" />
        ) : (
          <UserRoundX />
        )}
        Ignore
      </Button>
      {message && <span role="alert">{message}</span>}
    </form>
  );
}

function BackfillControl({
  appId,
  sources,
}: {
  appId: string;
  sources: string[];
}) {
  const [sourceInstanceId, setSourceInstanceId] = useState(sources[0] ?? "");
  const [pending, setPending] = useState(false);
  const [message, setMessage] = useState<string | null>(null);

  async function run(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!sourceInstanceId) return;
    setPending(true);
    setMessage(null);
    try {
      const response = await fetch(
        `/api/a/${encodeURIComponent(appId)}/identity/backfill`,
        {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ sourceInstanceId }),
        },
      );
      const payload = (await response.json()) as {
        error?: string;
        status?: "executed" | "queued";
        report?: { updated?: number; skippedObjects?: unknown[] } | null;
      };
      if (!response.ok) {
        setMessage(payload.error ?? "Ownership backfill could not be started.");
        return;
      }
      if (payload.status === "queued") {
        setMessage("Backfill is continuing in the background.");
      } else {
        const updated = payload.report?.updated ?? 0;
        const skipped = payload.report?.skippedObjects?.length ?? 0;
        setMessage(
          `${updated.toLocaleString("en")} record${updated === 1 ? "" : "s"} updated${skipped ? `; ${skipped} object${skipped === 1 ? "" : "s"} skipped` : ""}.`,
        );
      }
    } catch {
      setMessage("The identity service could not be reached.");
    } finally {
      setPending(false);
    }
  }

  return (
    <form className="records-identity-backfill" onSubmit={run}>
      <NativeSelect
        aria-label="Source instance to backfill"
        value={sourceInstanceId}
        onChange={(event) => setSourceInstanceId(event.target.value)}
        disabled={pending || sources.length === 0}
      >
        {sources.length === 0 ? (
          <option value="">No imported sources</option>
        ) : (
          sources.map((source) => (
            <option key={source} value={source}>
              {source}
            </option>
          ))
        )}
      </NativeSelect>
      <Button size="sm" type="submit" disabled={pending || !sourceInstanceId}>
        {pending ? <Spinner className="records-spin" /> : <RefreshCcw />}
        Re-run ownership
      </Button>
      {message && <span role="status">{message}</span>}
    </form>
  );
}

export function RecordIdentityPanel({
  appId,
  mappings,
  users,
  sourceInstances,
  counts,
  activeFilter,
}: Pick<
  RecordIdentityAdminModel,
  "appId" | "mappings" | "users" | "sourceInstances" | "counts"
> & {
  activeFilter: IdentityStatus | "all";
}) {
  return (
    <div className="records-identity-layout">
      <div className="records-identity-toolbar">
        <nav
          className="records-identity-filters"
          aria-label="Identity status filters"
        >
          {FILTERS.map((filter) => (
            <Link
              key={filter.value}
              className={
                activeFilter === filter.value ? "is-active" : undefined
              }
              href={
                filter.value === "all"
                  ? `/a/${appId}/admin/identity`
                  : `/a/${appId}/admin/identity?status=${filter.value}`
              }
            >
              {filter.label}
              <span>{counts[filter.value]}</span>
            </Link>
          ))}
        </nav>
        <BackfillControl appId={appId} sources={sourceInstances} />
      </div>

      <section
        className="records-identity-card"
        aria-label="Source identity mappings"
      >
        <header>
          <div>
            <h2>Source identities</h2>
            <p>
              Links are scoped to this app and source instance. Resolve
              conflicts before ownership is backfilled.
            </p>
          </div>
          <span>{mappings.length.toLocaleString("en")} shown</span>
        </header>
        <div className="records-identity-scroll">
          <Table className="records-identity-table">
            <TableHeader>
              <TableRow>
                <TableHead>Source user</TableHead>
                <TableHead>Source</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>OpenNeko user</TableHead>
                <TableHead>Resolution</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {mappings.map((mapping) => (
                <TableRow
                  key={`${mapping.sourceInstanceId}\u0000${mapping.sourceUserId}`}
                >
                  <TableCell>
                    <strong>{sourceLabel(mapping)}</strong>
                    {mapping.sourceName && <small>{mapping.sourceEmail}</small>}
                    <small>{mapping.sourceUserId}</small>
                  </TableCell>
                  <TableCell>
                    <code>{mapping.sourceInstanceId}</code>
                    {mapping.sourceIsActive === false && (
                      <small>Inactive at source</small>
                    )}
                  </TableCell>
                  <TableCell>
                    <span
                      className={`records-identity-status is-${mapping.status}`}
                    >
                      <StatusIcon status={mapping.status} />
                      {mapping.status}
                    </span>
                  </TableCell>
                  <TableCell>
                    {mapping.appUser ? (
                      <>
                        <strong>
                          {mapping.appUser.name ?? mapping.appUser.email}
                        </strong>
                        {mapping.appUser.name && (
                          <small>{mapping.appUser.email}</small>
                        )}
                      </>
                    ) : mapping.appUserId ? (
                      <>
                        <strong>Unavailable user</strong>
                        <small>{mapping.appUserId}</small>
                      </>
                    ) : (
                      "—"
                    )}
                  </TableCell>
                  <TableCell>
                    <MappingControl
                      appId={appId}
                      mapping={mapping}
                      users={users}
                    />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
        {mappings.length === 0 && (
          <div className="records-empty">
            <strong>No identities in this view</strong>
            <span>Try another status, or run a source import first.</span>
          </div>
        )}
      </section>
    </div>
  );
}
