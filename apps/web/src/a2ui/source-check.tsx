"use client";

import React from "react";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Disclosure } from "@/components/ui/disclosure";
import { formatCompact } from "@/lib/format-number";
import { registerComponent } from "./renderer";
import type { RenderContext } from "./renderer";
import type { SourceConnectionCheckProps } from "./catalog";
import type { A2UIComponent } from "./types";

export type SourceCheckFindingView = {
  step: string;
  status: "ok" | "warn" | "fail" | "skip";
  message: string;
  field?: string;
  sql?: string[];
};

export type SourceCheckView = {
  ok: boolean;
  supported: boolean;
  serverVersion?: string;
  findings: SourceCheckFindingView[];
  tables: Array<{ name: string; rows: number | null }>;
  moreTables: number;
};

const STATUS_LABEL: Record<SourceCheckFindingView["status"], string> = {
  ok: "Passed",
  warn: "Note",
  fail: "Failed",
  skip: "Not checked",
};

function selected(value: unknown): string {
  const item = Array.isArray(value) && value.length === 1 ? value[0] : value;
  if (typeof item === "number") return String(item);
  return typeof item === "string" ? item.trim() : "";
}

export function splitTableList(value: unknown): string[] {
  return typeof value === "string"
    ? value.split(",").map((name) => name.trim()).filter(Boolean)
    : [];
}

export function blockedTablesText(tables: readonly string[], chosen: ReadonlySet<string>, earlier: readonly string[] = []): string {
  const listed = new Set(tables);
  const blocked = [...earlier.filter((name) => !listed.has(name)), ...tables.filter((name) => !chosen.has(name))];
  return blocked.join(", ");
}

export function readSourceCheck(value: unknown): SourceCheckView | null {
  const check = (value as { check?: unknown } | null)?.check as Partial<SourceCheckView> | undefined;
  if (!check || !Array.isArray(check.findings) || !Array.isArray(check.tables)) return null;
  return {
    ok: check.ok === true,
    supported: check.supported !== false,
    serverVersion: typeof check.serverVersion === "string" ? check.serverVersion : undefined,
    findings: check.findings.filter((finding): finding is SourceCheckFindingView =>
      Boolean(finding) && typeof finding.message === "string" && typeof finding.status === "string"),
    tables: check.tables.filter((table) => table && typeof table.name === "string"),
    moreTables: typeof check.moreTables === "number" ? check.moreTables : 0,
  };
}

export function SourceCheckFindings({ check }: { check: SourceCheckView }) {
  return (
    <ul className="work-source-check-findings">
      {check.findings.map((finding) => (
        <li key={finding.step} className={`is-${finding.status}`}>
          <span className="work-source-check-status">{STATUS_LABEL[finding.status] ?? finding.status}</span>
          <div>
            <p>{finding.message}</p>
            {finding.sql?.length ? (
              <Disclosure title="SQL for the database administrator" open>
                <pre className="work-source-check-sql"><code>{finding.sql.join("\n")}</code></pre>
              </Disclosure>
            ) : null}
          </div>
        </li>
      ))}
    </ul>
  );
}

function SourceConnectionCheckView({
  props,
  ctx,
}: {
  props: SourceConnectionCheckProps & { id: string };
  ctx: RenderContext;
}) {
  const raw = ctx.surface.components.get(props.id)?.value;
  const path = raw && typeof raw === "object" && !Array.isArray(raw) && typeof (raw as { path?: unknown }).path === "string"
    ? (raw as { path: string }).path
    : null;
  const answers = {
    type: selected(props.type) || "postgres",
    host: selected(props.host),
    port: Number(selected(props.port)) || undefined,
    dbname: selected(props.dbname),
    user: selected(props.user),
    secretRef: selected(props.secretRef),
  };
  const key = JSON.stringify(answers);
  const ready = Boolean(answers.host && answers.dbname && answers.user && answers.secretRef);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
  const [result, setResult] = React.useState<{ key: string; check: SourceCheckView } | null>(null);
  const [chosen, setChosen] = React.useState<Set<string>>(new Set());
  const earlier = React.useRef<string[]>([]);

  const tableNames = React.useMemo(() => result?.check.tables.map((table) => table.name) ?? [], [result]);

  function choose(next: Set<string>) {
    setChosen(next);
    if (path) ctx.onDataChange?.(path, blockedTablesText(tableNames, next, earlier.current));
  }

  async function run() {
    setBusy(true);
    setError(null);
    try {
      const response = await fetch("/api/v1/settings/source-check", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: key,
      });
      const body = (await response.json().catch(() => ({}))) as { error?: string };
      const check = readSourceCheck(body);
      if (!response.ok || !check) throw new Error(body.error || "The check did not run.");
      earlier.current = splitTableList(props.value);
      const blocked = new Set(earlier.current);
      setResult({ key, check });
      setChosen(new Set(check.tables.map((table) => table.name).filter((name) => !blocked.has(name))));
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "The check did not run.");
    } finally {
      setBusy(false);
    }
  }

  const check = result?.check;
  const stale = Boolean(result && result.key !== key);
  return (
    <div className="work-openapi-import work-source-check">
      <span className="work-a2ui-label">{props.label ?? "Check connection"}</span>
      <p className="work-managed-files-note">
        OpenNeko tests the host, the port, TLS, the login and its rights, then lists the tables the login can read.
      </p>
      <Button type="button" className="work-a2ui-button" disabled={busy || !ready} onClick={() => void run()}>
        {busy ? "Checking…" : check ? "Check again" : "Check connection"}
      </Button>
      {!ready ? <p className="work-managed-files-note">Enter the host, database and login, and save the password first.</p> : null}
      {error ? <div className="work-openapi-error" role="alert">{error}</div> : null}
      {stale ? <div className="work-openapi-error" role="alert">The answers changed after the check. Check again.</div> : null}
      {check ? <SourceCheckFindings check={check} /> : null}
      {check?.ok && check.tables.length > 0 ? (
        <fieldset className="work-source-check-tables">
          <legend className="work-a2ui-label">Tables OpenNeko may read</legend>
          <div className="work-source-check-tables-head">
            <span>{chosen.size} of {check.tables.length} selected</span>
            <Button type="button" variant="ghost" size="sm" onClick={() => choose(new Set(tableNames))}>Select all</Button>
            <Button type="button" variant="ghost" size="sm" onClick={() => choose(new Set())}>Clear all</Button>
          </div>
          <ul>
            {check.tables.map((table) => (
              <li key={table.name}>
                <Checkbox
                  label={table.name}
                  checked={chosen.has(table.name)}
                  onCheckedChange={(value) => {
                    const next = new Set(chosen);
                    if (value === true) next.add(table.name);
                    else next.delete(table.name);
                    choose(next);
                  }}
                />
                {table.rows !== null ? <code>{formatCompact(table.rows)} rows</code> : null}
              </li>
            ))}
          </ul>
          {check.moreTables > 0 ? (
            <p className="work-managed-files-note">
              The login can read {check.moreTables} more tables. They stay readable; block them with a later change.
            </p>
          ) : null}
          <p className="work-managed-files-note">Tables you clear go into the list of tables no one may read.</p>
        </fieldset>
      ) : null}
    </div>
  );
}

registerComponent(
  "SourceConnectionCheck",
  (comp: A2UIComponent, ctx: RenderContext) => (
    <SourceConnectionCheckView
      props={comp as unknown as SourceConnectionCheckProps & { id: string }}
      ctx={ctx}
    />
  ),
);
