import Link from "next/link";
import { notFound } from "next/navigation";
import { ArrowLeft, Search } from "lucide-react";
import { getOrgId } from "@/lib/db";
import { readRecordRecycleList, RecordAppRouteError } from "@/lib/records";
import { RecordRecycleTable } from "@/components/records/RecordRecycleTable";
import { RecordsUnavailable } from "@/components/records/RecordsNotice";
import { Input } from "@/components/ui/input";

export const dynamic = "force-dynamic";

type SearchParams = Record<string, string | string[] | undefined>;

function value(params: SearchParams, key: string): string | undefined {
  const candidate = params[key];
  return typeof candidate === "string" ? candidate : undefined;
}

function positivePage(raw: string | undefined): number {
  const parsed = Number.parseInt(raw ?? "1", 10);
  return Number.isSafeInteger(parsed) && parsed > 0 && parsed < 10_000
    ? parsed
    : 1;
}

export default async function RecordRecyclePage({
  params,
  searchParams,
}: {
  params: Promise<{ app: string; object: string }>;
  searchParams: Promise<SearchParams>;
}) {
  const [{ app, object }, queryParams, orgId] = await Promise.all([
    params,
    searchParams,
    getOrgId(),
  ]);
  let result: Awaited<ReturnType<typeof readRecordRecycleList>> | null = null;
  let routeError: RecordAppRouteError | null = null;
  try {
    result = await readRecordRecycleList({
      orgId,
      appId: app,
      objectApiName: object,
      first: 50,
      after: value(queryParams, "after"),
      search: value(queryParams, "q"),
    });
  } catch (error) {
    if (error instanceof RecordAppRouteError) {
      routeError = error;
    } else {
      throw error;
    }
  }

  if (routeError?.status === 404) notFound();
  if (routeError) return <RecordsUnavailable message={routeError.message} />;
  if (!result) throw new Error("Recycle page did not resolve.");
  const base = `/a/${result.app.appId}/${result.view.object.apiName}`;
  const recycleBase = `${base}/recycle`;
  const page = positivePage(value(queryParams, "page"));
  const query = {
    q: value(queryParams, "q"),
    after: value(queryParams, "after"),
    page: value(queryParams, "page"),
  };
  return (
    <main className="records-root">
      <header className="records-object-header">
        <Link
          className="records-back"
          href={base}
          aria-label={`Back to ${result.view.object.pluralLabel}`}
        >
          <ArrowLeft aria-hidden="true" />
        </Link>
        <div className="records-object-title">
          <span className="records-breadcrumb">{result.app.label}</span>
          <span aria-hidden="true">/</span>
          <h1>Recycle bin</h1>
          <span className="records-total">
            {result.total.toLocaleString("en")} deleted
          </span>
        </div>
        <form className="records-search" action={recycleBase} method="get">
          <Search aria-hidden="true" />
          <Input
            type="search"
            name="q"
            defaultValue={query.q}
            placeholder={`Search deleted ${result.view.object.pluralLabel.toLowerCase()}`}
            aria-label={`Search deleted ${result.view.object.pluralLabel}`}
          />
        </form>
      </header>
      <div className="records-recycle-note">
        <strong>{result.view.object.pluralLabel}</strong>
        <span>
          Only restore identity and deletion metadata are exposed here.
        </span>
      </div>
      <RecordRecycleTable
        appId={result.app.appId}
        objectApiName={result.view.object.apiName}
        objectPluralLabel={result.view.object.pluralLabel}
        rows={result.rows}
        owners={result.owners}
        total={result.total}
        cursor={result.cursor}
        page={page}
        query={query}
      />
    </main>
  );
}
