"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { CheckCircle2, LockKeyhole } from "lucide-react";
import type { RecordViewColumn } from "@neko/records";
import { Button, buttonClassName } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { Checkbox } from "@/components/ui/checkbox";
import { Input, NativeSelect, Textarea } from "@/components/ui/field";

type SubmitResponse = {
  status?: "executed" | "queued";
  actionRequestId?: string;
  recordId?: string | null;
  policy?: string;
  error?: string;
  issues?: Array<{ field: string; message: string }>;
};

function picklistOptions(
  column: RecordViewColumn,
): Array<{ value: string; label: string }> {
  return (column.picklistValues ?? []).flatMap((option) => {
    if (typeof option === "string") return [{ value: option, label: option }];
    if (option && typeof option === "object" && !Array.isArray(option)) {
      const entry = option as Record<string, unknown>;
      if (typeof entry.value === "string") {
        return [
          {
            value: entry.value,
            label: typeof entry.label === "string" ? entry.label : entry.value,
          },
        ];
      }
    }
    return [];
  });
}

function stringValue(value: unknown): string {
  return value === null || value === undefined ? "" : String(value);
}

function datetimeLocalValue(value: unknown): string {
  const date = new Date(stringValue(value));
  if (Number.isNaN(date.getTime())) return "";
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

function multiValue(value: unknown): string[] {
  if (Array.isArray(value)) return value.map(String);
  if (typeof value === "string") {
    try {
      const parsed = JSON.parse(value) as unknown;
      return Array.isArray(parsed) ? parsed.map(String) : [];
    } catch {
      return value ? [value] : [];
    }
  }
  return [];
}

function FieldControl({
  column,
  value,
}: {
  column: RecordViewColumn;
  value: unknown;
}): ReactNode {
  const common = {
    id: `record-field-${column.apiName}`,
    name: column.apiName,
    required: column.required,
  };
  if (column.readOnly || column.kind === "readonly_formula") {
    return (
      <Input
        {...common}
        type="text"
        value={stringValue(value)}
        disabled
        readOnly
      />
    );
  }
  if (column.kind === "textarea") {
    return <Textarea {...common} defaultValue={stringValue(value)} rows={5} />;
  }
  if (column.kind === "boolean") {
    return (
      <Checkbox {...common} label="Enabled" defaultChecked={value === true} />
    );
  }
  if (column.kind === "picklist") {
    return (
      <NativeSelect {...common} defaultValue={stringValue(value)}>
        {!column.required && <option value="">Not set</option>}
        {picklistOptions(column).map((option) => (
          <option value={option.value} key={option.value}>
            {option.label}
          </option>
        ))}
      </NativeSelect>
    );
  }
  if (column.kind === "multipicklist") {
    return (
      <NativeSelect
        {...common}
        multiple
        defaultValue={multiValue(value)}
        size={Math.min(5, Math.max(3, picklistOptions(column).length))}
      >
        {picklistOptions(column).map((option) => (
          <option value={option.value} key={option.value}>
            {option.label}
          </option>
        ))}
      </NativeSelect>
    );
  }
  const type =
    column.kind === "email"
      ? "email"
      : column.kind === "phone"
        ? "tel"
        : column.kind === "url"
          ? "url"
          : column.kind === "date"
            ? "date"
            : column.kind === "datetime"
              ? "datetime-local"
              : ["integer", "decimal", "currency", "percent"].includes(
                    column.kind,
                  )
                ? "number"
                : "text";
  return (
    <Input
      {...common}
      type={type}
      defaultValue={
        column.kind === "datetime"
          ? datetimeLocalValue(value)
          : stringValue(value)
      }
      {...(column.kind === "integer" ? { step: 1 } : {})}
      {...(["decimal", "currency", "percent"].includes(column.kind)
        ? { step: column.scale === null ? "any" : 10 ** -column.scale }
        : {})}
      {...(column.kind === "reference" ? { spellCheck: false } : {})}
    />
  );
}

type ReferenceOption = { id: string; label: string };

function ReferenceControl({
  appId,
  column,
  value,
}: {
  appId: string;
  column: RecordViewColumn;
  value: unknown;
}) {
  const targets = column.referenceTargets ?? [];
  const [target, setTarget] = useState(targets[0] ?? "");
  const [query, setQuery] = useState(stringValue(value));
  const [options, setOptions] = useState<ReferenceOption[]>([]);
  const listId = `record-reference-${column.apiName}`;

  useEffect(() => {
    const search = query.trim();
    if (!target || !search) {
      return;
    }
    const controller = new AbortController();
    const timeout = window.setTimeout(() => {
      fetch(
        `/api/a/${encodeURIComponent(appId)}/references/${encodeURIComponent(target)}?q=${encodeURIComponent(search)}`,
        { signal: controller.signal },
      )
        .then(async (response) =>
          response.ok
            ? ((await response.json()) as { options?: ReferenceOption[] })
            : { options: [] },
        )
        .then((payload) => setOptions(payload.options ?? []))
        .catch((error: unknown) => {
          if (!(error instanceof DOMException && error.name === "AbortError"))
            setOptions([]);
        });
    }, 180);
    return () => {
      window.clearTimeout(timeout);
      controller.abort();
    };
  }, [appId, query, target]);

  return (
    <div className="records-reference-control">
      {targets.length > 1 && (
        <NativeSelect
          aria-label={`${column.label} target object`}
          value={target}
          onChange={(event) => {
            setTarget(event.target.value);
            setQuery("");
            setOptions([]);
          }}
        >
          {targets.map((candidate) => (
            <option value={candidate} key={candidate}>
              {candidate}
            </option>
          ))}
        </NativeSelect>
      )}
      <Input
        id={`record-field-${column.apiName}`}
        name={column.apiName}
        required={column.required}
        value={query}
        onChange={(event) => {
          setQuery(event.target.value);
          if (!event.target.value.trim()) setOptions([]);
        }}
        list={listId}
        autoComplete="off"
        role="combobox"
        aria-autocomplete="list"
        aria-controls={listId}
        aria-expanded={options.length > 0}
        placeholder="Search by record name or enter an ID"
        spellCheck={false}
      />
      <datalist id={listId}>
        {options.map((option) => (
          <option value={option.id} key={option.id}>
            {option.label}
          </option>
        ))}
      </datalist>
    </div>
  );
}

function submittedValue(formData: FormData, column: RecordViewColumn): unknown {
  if (column.kind === "boolean") return formData.has(column.apiName);
  if (column.kind === "multipicklist") {
    return formData.getAll(column.apiName).map(String);
  }
  const raw = formData.get(column.apiName);
  const value = typeof raw === "string" ? raw : "";
  if (value === "") return undefined;
  if (column.kind === "integer") return Number(value);
  if (column.kind === "datetime") return new Date(value).toISOString();
  return value;
}

export function RecordForm({
  appId,
  objectApiName,
  objectLabel,
  operation,
  fields,
  initialRow,
  recordId,
}: {
  appId: string;
  objectApiName: string;
  objectLabel: string;
  operation: "create" | "update";
  fields: RecordViewColumn[];
  initialRow?: Record<string, unknown> | null;
  recordId?: string | null;
}) {
  const router = useRouter();
  const [submitting, setSubmitting] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [actionRequestId, setActionRequestId] = useState<string | null>(null);
  const base = `/a/${appId}/${objectApiName}`;

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSubmitting(true);
    setMessage(null);
    setActionRequestId(null);
    const formData = new FormData(event.currentTarget);
    const values: Record<string, unknown> = {};
    const expected: Record<string, unknown> = {};
    for (const column of fields) {
      if (column.readOnly || column.kind === "readonly_formula") continue;
      const value = submittedValue(formData, column);
      if (value !== undefined || operation === "update") {
        values[column.apiName] = value ?? null;
      }
      if (operation === "update") {
        expected[column.apiName] = initialRow?.[column.columnName] ?? null;
      }
    }

    try {
      const response = await fetch(`/api/a/${appId}/${objectApiName}/submit`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          operation,
          ...(operation === "update" ? { id: recordId, expected } : {}),
          fields: values,
        }),
      });
      const payload = (await response.json()) as SubmitResponse;
      if (!response.ok) {
        const issueText = payload.issues
          ?.map((issue) => `${issue.field}: ${issue.message}`)
          .join(" · ");
        setMessage(
          issueText || payload.error || "The record could not be submitted.",
        );
        setActionRequestId(payload.actionRequestId ?? null);
        return;
      }
      if (payload.status === "queued") {
        setMessage(
          "The governed action is still queued. No success was assumed; you can inspect its status below.",
        );
        setActionRequestId(payload.actionRequestId ?? null);
        return;
      }
      const destinationId = payload.recordId ?? recordId;
      if (!destinationId) {
        setMessage(
          "The action executed, but the worker did not return a record ID.",
        );
        setActionRequestId(payload.actionRequestId ?? null);
        return;
      }
      router.push(`${base}/${encodeURIComponent(destinationId)}`);
      router.refresh();
    } catch {
      setMessage(
        "The records service could not be reached. The form was not reported as successful.",
      );
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <form className="records-form-card" onSubmit={submit}>
      <div className="records-form-grid">
        {fields.map((column) => (
          <div
            className={`records-form-field${column.kind === "textarea" ? " is-wide" : ""}`}
            key={column.apiName}
          >
            <label htmlFor={`record-field-${column.apiName}`}>
              {column.label}
              {column.required && <span aria-label="required"> *</span>}
            </label>
            {column.kind === "reference" && !column.readOnly ? (
              <ReferenceControl
                appId={appId}
                column={column}
                value={initialRow?.[column.columnName]}
              />
            ) : (
              <FieldControl
                column={column}
                value={initialRow?.[column.columnName]}
              />
            )}
            {column.kind === "reference" && (
              <small>
                Search the permitted target object by name; the selected record
                ID is submitted.
              </small>
            )}
            {(column.readOnly || column.kind === "readonly_formula") && (
              <small>
                Calculated or source-managed value; shown for context only.
              </small>
            )}
            {column.kind === "multipicklist" && (
              <small>Use Ctrl or Command to select more than one value.</small>
            )}
          </div>
        ))}
      </div>
      {fields.length === 0 && (
        <div className="records-form-empty">
          This object has no editable fields.
        </div>
      )}
      {message && (
        <div className="records-form-message" role="status">
          <span>{message}</span>
          {actionRequestId && (
            <Link href={`/actions/${actionRequestId}`}>
              View action request
            </Link>
          )}
        </div>
      )}
      <footer className="records-form-footer">
        <p>
          <LockKeyhole aria-hidden="true" />
          Submitted through action policy, registry validation, and durable
          audit history.
        </p>
        <Link
          className={buttonClassName({
            size: "sm",
            className: "records-form-cancel",
          })}
          href={recordId ? `${base}/${encodeURIComponent(recordId)}` : base}
        >
          Cancel
        </Link>
        <Button
          className="records-primary-action"
          variant="primary"
          size="sm"
          type="submit"
          disabled={submitting || fields.length === 0}
        >
          {submitting ? (
            <Spinner className="records-spin" aria-hidden="true" />
          ) : (
            <CheckCircle2 aria-hidden="true" />
          )}
          {submitting
            ? "Submitting…"
            : `${operation === "create" ? "Create" : "Save"} ${objectLabel.toLowerCase()}`}
        </Button>
      </footer>
    </form>
  );
}
