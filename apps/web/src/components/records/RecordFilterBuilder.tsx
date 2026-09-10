"use client";

import { useRouter } from "next/navigation";
import { useMemo, useState, type FormEvent } from "react";
import type {
  RecordFieldKind,
  RecordFilterExpression,
  RecordListFilter,
} from "@neko/records";
import { Button } from "@/components/ui/button";
import { Disclosure } from "@/components/ui/disclosure";
import { Input, NativeSelect } from "@/components/ui/field";

export type RecordFilterField = {
  apiName: string;
  label: string;
  kind: RecordFieldKind;
  picklistValues: unknown[] | null;
};

function queryParams(query: Record<string, string | undefined>): URLSearchParams {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && key !== "after" && key !== "page") params.set(key, value);
  }
  return params;
}

function existingFilter(raw: string | undefined): RecordFilterExpression | null {
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as unknown;
    return parsed && typeof parsed === "object"
      ? (parsed as RecordFilterExpression)
      : null;
  } catch {
    return null;
  }
}

function valueFor(field: RecordFilterField, raw: string): unknown {
  if (field.kind === "boolean") return raw === "true";
  if (["integer", "decimal", "currency", "percent"].includes(field.kind)) {
    const numeric = Number(raw);
    return Number.isFinite(numeric) ? numeric : raw;
  }
  return raw;
}

function options(field: RecordFilterField | undefined): Array<{ value: string; label: string }> {
  return (field?.picklistValues ?? []).flatMap((entry) => {
    if (typeof entry === "string") return [{ value: entry, label: entry }];
    if (entry && typeof entry === "object" && !Array.isArray(entry)) {
      const option = entry as Record<string, unknown>;
      return typeof option.value === "string"
        ? [{
            value: option.value,
            label: typeof option.label === "string" ? option.label : option.value,
          }]
        : [];
    }
    return [];
  });
}

export function RecordFilterBuilder({
  base,
  query,
  fields,
}: {
  base: string;
  query: Record<string, string | undefined>;
  fields: RecordFilterField[];
}) {
  const router = useRouter();
  const [fieldName, setFieldName] = useState(fields[0]?.apiName ?? "");
  const [operator, setOperator] = useState("eq");
  const selected = useMemo(
    () => fields.find((field) => field.apiName === fieldName),
    [fieldName, fields],
  );
  const picklistOptions = options(selected);
  const noValue = ["is_open", "is_closed", "this_quarter"].includes(operator);

  function apply(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selected) return;
    const form = new FormData(event.currentTarget);
    const raw = String(form.get("value") ?? "").trim();
    let leaf: RecordListFilter;
    if (operator === "this_quarter") {
      leaf = {
        field: selected.apiName,
        operator: "relative",
        value: { macro: "this_quarter" },
      };
    } else if (operator === "last_n_days") {
      leaf = {
        field: selected.apiName,
        operator: "relative",
        value: { macro: "last_n_days", days: Number(raw) },
      };
    } else if (operator === "is_open" || operator === "is_closed") {
      leaf = { field: selected.apiName, operator };
    } else if (operator === "is_null") {
      leaf = { field: selected.apiName, operator: "is_null", value: raw === "true" };
    } else if (operator === "in") {
      leaf = {
        field: selected.apiName,
        operator: "in",
        value: raw.split(",").map((item) => item.trim()).filter(Boolean),
      };
    } else {
      leaf = {
        field: selected.apiName,
        operator: operator as RecordListFilter["operator"],
        value: valueFor(selected, raw),
      };
    }
    const current = existingFilter(query.filter);
    const filter: RecordFilterExpression = current
      ? current && "op" in current && current.op === "and"
        ? { ...current, clauses: [...current.clauses, leaf] }
        : { op: "and", clauses: [current, leaf] }
      : leaf;
    const params = queryParams(query);
    params.set("filter", JSON.stringify(filter));
    router.push(`${base}?${params.toString()}`);
  }

  return (
    <Disclosure title="Add filter" className="records-filter-builder">
      <form onSubmit={apply} className="grid gap-3">
        <label className="grid gap-1.5 font-body text-ui-caption font-bold text-text2">
          Field
          <NativeSelect value={fieldName} onChange={(event) => setFieldName(event.target.value)}>
            {fields.map((field) => (
              <option value={field.apiName} key={field.apiName}>{field.label}</option>
            ))}
          </NativeSelect>
        </label>
        <label className="grid gap-1.5 font-body text-ui-caption font-bold text-text2">
          Condition
          <NativeSelect value={operator} onChange={(event) => setOperator(event.target.value)}>
            <option value="eq">equals</option>
            <option value="neq">does not equal</option>
            <option value="contains">contains</option>
            <option value="starts_with">starts with</option>
            <option value="in">is one of</option>
            <option value="is_null">is empty</option>
            <option value="gt">is greater than</option>
            <option value="gte">is at least</option>
            <option value="lt">is less than</option>
            <option value="lte">is at most</option>
            {(selected?.kind === "date" || selected?.kind === "datetime") && (
              <>
                <option value="this_quarter">is this quarter</option>
                <option value="last_n_days">is in the last N days</option>
              </>
            )}
            {selected?.kind === "picklist" && (
              <>
                <option value="is_open">is semantically open</option>
                <option value="is_closed">is semantically closed</option>
              </>
            )}
          </NativeSelect>
        </label>
        {!noValue && (
          <label className="grid gap-1.5 font-body text-ui-caption font-bold text-text2">
            Value
            {picklistOptions.length > 0 && ["eq", "neq"].includes(operator) ? (
              <NativeSelect name="value">
                {picklistOptions.map((option) => (
                  <option value={option.value} key={option.value}>{option.label}</option>
                ))}
              </NativeSelect>
            ) : operator === "is_null" ? (
              <NativeSelect name="value">
                <option value="true">is empty</option>
                <option value="false">is not empty</option>
              </NativeSelect>
            ) : (
              <Input
                name="value"
                required
                type={operator === "last_n_days" ? "number" : "text"}
                min={operator === "last_n_days" ? 1 : undefined}
                max={operator === "last_n_days" ? 3650 : undefined}
                placeholder={operator === "in" ? "value one, value two" : undefined}
              />
            )}
          </label>
        )}
        <Button variant="primary" size="sm" type="submit">
          Apply filter
        </Button>
      </form>
    </Disclosure>
  );
}
