import Link from "next/link";
import type {
  RecordFilterExpression,
  RecordListFilter,
  RecordFilterOperator,
  RecordSavedView,
  RecordSavedViewDefinition,
} from "@neko/records";
import {
  RecordFilterBuilder,
  type RecordFilterField,
} from "./RecordFilterBuilder";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Disclosure } from "@/components/ui/disclosure";
import { Input, NativeSelect } from "@/components/ui/field";

function hrefWith(base: string, current: Record<string, string | undefined>, mine: boolean) {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(current)) {
    if (value !== undefined && key !== "after" && key !== "page") params.set(key, value);
  }
  if (mine) params.set("mine", "true");
  else params.set("mine", "false");
  const query = params.toString();
  return query ? `${base}?${query}` : base;
}

export function RecordViewBar({
  base,
  objectLabel,
  query,
  ownerScoped,
  appId,
  objectApiName,
  views,
  selectedView,
  definition,
  fields,
  canShare,
}: {
  base: string;
  objectLabel: string;
  query: Record<string, string | undefined>;
  ownerScoped: boolean;
  appId: string;
  objectApiName: string;
  views: RecordSavedView[];
  selectedView: RecordSavedView | null;
  definition: RecordSavedViewDefinition;
  fields: RecordFilterField[];
  canShare: boolean;
}) {
  const mine = query.mine === "true";
  const endpoint = `/api/a/${encodeURIComponent(appId)}/${encodeURIComponent(objectApiName)}/views`;
  const activeFilter = definition.filter;
  return (
    <div className="records-viewbar">
      <form className="records-view-picker" action={base} method="get">
        <label className="sr-only" htmlFor="records-saved-view">Saved view</label>
        <NativeSelect id="records-saved-view" name="view" defaultValue={selectedView?.id ?? ""}>
          <option value="">All {objectLabel.toLowerCase()}</option>
          {views.map((view) => (
            <option value={view.id} key={view.id}>
              {view.label}{view.shared ? " · shared" : ""}
            </option>
          ))}
        </NativeSelect>
        <Button size="sm" type="submit">Open</Button>
      </form>
      {query.q && (
        <span className="records-filter-chip">
          Name contains <b>{query.q}</b>
          <Link href={hrefWith(base, { ...query, q: "" }, mine)} aria-label="Clear search">
            ×
          </Link>
        </span>
      )}
      {activeFilter && (
        <>
          {filterSummaries(activeFilter, fields).map((summary) => (
            <span className="records-filter-chip" key={summary.key}>
              {summary.field} <b>{summary.value}</b>
            </span>
          ))}
          <Link
            className="records-filter-clear"
            href={hrefWith(base, { ...query, filter: "null" }, mine)}
            aria-label="Clear all filters"
          >
            ×
          </Link>
        </>
      )}
      <RecordFilterBuilder base={base} query={query} fields={fields} />
      <Disclosure title="Save view" className="records-view-save">
        <form action={endpoint} method="post" className="grid gap-3">
          <label className="grid gap-1.5 font-body text-ui-caption font-bold text-text2">
            Name
            <Input name="label" required maxLength={80} defaultValue={selectedView?.label} />
          </label>
          <input
            type="hidden"
            name="definition"
            value={JSON.stringify(definition)}
            data-ui-bespoke-reason="hidden view definition payload"
          />
          {canShare && (
            <Checkbox
              name="shared"
              value="true"
              defaultChecked={selectedView?.shared}
              label="Share with this organization"
            />
          )}
          <Button variant="primary" size="sm" type="submit">Save</Button>
        </form>
      </Disclosure>
      {selectedView && (!selectedView.shared || canShare) && (
        <form action={`${endpoint}/${selectedView.id}`} method="post">
          <Button
            variant="danger"
            size="sm"
            type="submit"
            aria-label={`Delete ${selectedView.label}`}
          >
            Delete view
          </Button>
        </form>
      )}
      {ownerScoped && (
        <Link
          href={hrefWith(base, query, !mine)}
          className="records-scope-toggle"
          role="switch"
          aria-checked={mine}
        >
          My records
          <span className={`records-switch${mine ? " is-on" : ""}`} aria-hidden="true">
            <span />
          </span>
        </Link>
      )}
    </div>
  );
}

function filterLeaves(expression: RecordFilterExpression): RecordFilterExpression[] {
  return "op" in expression
    ? expression.clauses.flatMap(filterLeaves)
    : [expression];
}

function optionLabel(field: RecordFilterField | undefined, value: unknown): string {
  for (const option of field?.picklistValues ?? []) {
    if (typeof option === "string" && option === value) return option;
    if (option && typeof option === "object" && !Array.isArray(option)) {
      const candidate = option as Record<string, unknown>;
      if (candidate.value === value && typeof candidate.label === "string") {
        return candidate.label;
      }
    }
  }
  if (Array.isArray(value)) return value.map((item) => optionLabel(field, item)).join(", ");
  return String(value ?? "");
}

function filterValue(
  filter: RecordListFilter,
  field: RecordFilterField | undefined,
): string {
  if (filter.operator === "is_open") return "is open";
  if (filter.operator === "is_closed") return "is closed";
  if (filter.operator === "is_null") return filter.value ? "is empty" : "is set";
  if (filter.operator === "relative") {
    const value = filter.value && typeof filter.value === "object" && !Array.isArray(filter.value)
      ? filter.value as Record<string, unknown>
      : {};
    if (value.macro === "this_quarter") return "this quarter";
    if (value.macro === "last_n_days") return `in the last ${String(value.days)} days`;
  }
  const operatorLabels: Partial<Record<RecordFilterOperator, string>> = {
    eq: "is",
    neq: "is not",
    in: "is one of",
    not_in: "is not one of",
    contains: "contains",
    starts_with: "starts with",
    gt: "is greater than",
    gte: "is at least",
    lt: "is less than",
    lte: "is at most",
  };
  const operator = operatorLabels[filter.operator] ??
    filter.operator.replaceAll("_", " ");
  const value = optionLabel(field, filter.value);
  return value ? `${operator} ${value}` : operator;
}

function filterSummaries(
  expression: RecordFilterExpression,
  fields: RecordFilterField[],
): Array<{ key: string; field: string; value: string }> {
  const byName = new Map(fields.map((field) => [field.apiName, field]));
  const leaves = filterLeaves(expression);
  const summaries = leaves.slice(0, 3).flatMap((leaf, index) => {
    if ("op" in leaf) return [];
    const field = byName.get(leaf.field);
    return [{
      key: `${leaf.field}-${leaf.operator}-${index}`,
      field: field?.label ?? leaf.field.replaceAll("_", " "),
      value: filterValue(leaf, field),
    }];
  });
  if (leaves.length > 3) {
    summaries.push({
      key: "remaining-filters",
      field: `+${leaves.length - 3}`,
      value: "more",
    });
  }
  return summaries;
}
