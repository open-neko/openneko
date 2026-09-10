"use client";

import Link from "next/link";
import { useState } from "react";
import {
  ArrowRight,
  Database,
  Plus,
  RefreshCw,
} from "lucide-react";
import { buttonClassName } from "@/components/ui/button";
import { SearchInput } from "@/components/ui/search-input";
import { matchesListSearch } from "@/lib/list-search";
import type { RecordAppNavItem } from "@/lib/records";

const CREATE_APP_PROMPT =
  "Help me create a new app. Start by asking what workflow it should support, who will use it, and which records they need to manage.";

export function createAppHref(): string {
  return `/work?seed=${encodeURIComponent(CREATE_APP_PROMPT)}`;
}

function objectSummary(app: RecordAppNavItem): string {
  const visible = app.objects.slice(0, 4).map((object) => object.pluralLabel);
  const remaining = app.objects.length - visible.length;
  return remaining > 0
    ? `${visible.join(", ")} +${remaining} more`
    : visible.join(", ");
}

export function AppsOverview({
  apps,
  canCreate,
  unavailable = false,
}: {
  apps: RecordAppNavItem[];
  canCreate: boolean;
  unavailable?: boolean;
}) {
  const createHref = createAppHref();
  const [query, setQuery] = useState("");
  const visibleApps = apps.filter((app) =>
    matchesListSearch(
      query,
      app.label,
      app.purpose,
      ...app.objects.flatMap((object) => [object.label, object.pluralLabel]),
    ),
  );

  return (
    <main className="apps-overview-root">
      <header className="apps-overview-header">
        <div>
          <h1>Your workspaces</h1>
          <p>
            Governed record systems available to you. Access follows your app,
            object, and field permissions.
          </p>
        </div>
        {canCreate && !unavailable && (
          <Link
            className={buttonClassName({
              variant: "primary",
              className: "apps-overview-create",
            })}
            href={createHref}
          >
            <Plus aria-hidden="true" />
            Create app
          </Link>
        )}
      </header>

      {apps.length > 0 && !unavailable ? (
        <div className="mb-6 max-w-[520px]">
          <SearchInput
            label="Search apps"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search apps and record types"
          />
        </div>
      ) : null}

      {unavailable ? (
        <section className="apps-overview-empty" aria-labelledby="apps-unavailable-title">
          <span className="apps-overview-empty-icon"><RefreshCw aria-hidden="true" /></span>
          <h2 id="apps-unavailable-title">Apps are temporarily unavailable</h2>
          <p>The app registry could not be reached. Your access has not changed.</p>
          <Link
            href="/apps"
            className={buttonClassName({ size: "sm", className: "mt-[18px]" })}
          >
            Try again
          </Link>
        </section>
      ) : visibleApps.length > 0 ? (
        <section className="apps-overview-section" aria-labelledby="accessible-apps-title">
          <div className="apps-overview-section-heading">
            <h2 id="accessible-apps-title">Available to you</h2>
            <span>
              {visibleApps.length} {visibleApps.length === 1 ? "app" : "apps"}
            </span>
          </div>
          <div className="apps-overview-list">
            {visibleApps.map((app) => (
              <Link
                className="apps-overview-row"
                href={`/a/${app.appId}`}
                key={app.appId}
              >
                <span className="apps-overview-app-icon"><Database aria-hidden="true" /></span>
                <span className="apps-overview-app-copy">
                  <strong>{app.label}</strong>
                  <span>{app.purpose?.trim() || "No description has been added yet."}</span>
                  <small>
                    {app.objects.length} {app.objects.length === 1 ? "object" : "objects"}
                    {app.objects.length > 0 ? ` · ${objectSummary(app)}` : ""}
                  </small>
                </span>
                <span className="apps-overview-open">
                  Open app <ArrowRight aria-hidden="true" />
                </span>
              </Link>
            ))}
          </div>
        </section>
      ) : query ? (
        <section className="apps-overview-empty" aria-labelledby="empty-apps-title">
          <span className="apps-overview-empty-icon"><Database aria-hidden="true" /></span>
          <h2 id="empty-apps-title">No matching apps</h2>
          <p>Try another app name, purpose, or record type.</p>
        </section>
      ) : canCreate ? (
        <section className="apps-overview-empty" aria-labelledby="empty-apps-title">
          <span className="apps-overview-empty-icon"><Database aria-hidden="true" /></span>
          <h2 id="empty-apps-title">Create your first app</h2>
          <p>
            Apps group related business records, layouts, permissions, and
            contextual chat into one governed workspace.
          </p>
          <ol className="apps-overview-steps">
            <li><span>1</span><strong>Describe the workflow</strong><small>Explain who uses it and what they manage.</small></li>
            <li><span>2</span><strong>Review the design</strong><small>OpenNeko proposes objects, fields, and relationships.</small></li>
            <li><span>3</span><strong>Approve and launch</strong><small>The governed schema change is applied after approval.</small></li>
          </ol>
          <Link
            className={buttonClassName({
              variant: "primary",
              className: "apps-overview-create",
            })}
            href={createHref}
          >
            <Plus aria-hidden="true" />
            Describe an app
          </Link>
        </section>
      ) : (
        <section className="apps-overview-empty" aria-labelledby="empty-apps-title">
          <span className="apps-overview-empty-icon"><Database aria-hidden="true" /></span>
          <h2 id="empty-apps-title">No apps assigned</h2>
          <p>
            Apps are governed workspaces for related business records. An
            administrator can grant you access to an app and its objects.
          </p>
          <span className="apps-overview-member-note">
            Contact your OpenNeko administrator to request access.
          </span>
        </section>
      )}
    </main>
  );
}
