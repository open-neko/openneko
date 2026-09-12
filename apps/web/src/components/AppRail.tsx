"use client";

import Image from "next/image";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useEffect, useMemo, useRef, useState } from "react";
import {
  Database,
  LayoutGrid,
  LogOut,
  Search,
  Settings2,
  Star,
  Table2,
} from "lucide-react";
import DensityToggle from "@/components/DensityToggle";
import { Button } from "@/components/ui/button";
import {
  ALL_NAV,
  hideAppChrome,
  isActive,
  useApprovalsCount,
  type NavDestination,
} from "@/lib/nav";
import { buildRecordNavSections, type RecordNavObject } from "@/lib/record-nav";
import { RECORDS_VISUAL_NAV_APPS } from "@/lib/records-visual-nav";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";

const APP_VERSION = process.env.NEXT_PUBLIC_APP_VERSION ?? "0.0.0";
const VERSION_POLL_MS = 60_000;
const RECORDS_VISUAL_TEST =
  process.env.NODE_ENV !== "production" &&
  process.env.NEXT_PUBLIC_RECORDS_VISUAL_TEST === "true";

type SessionUser = {
  email: string;
  name: string | null;
};

type RecordAppNav = {
  appId: string;
  label: string;
  purpose: string | null;
  objects: RecordNavObject[];
};

function storedObjectIds(key: string): string[] {
  try {
    const parsed = JSON.parse(window.localStorage.getItem(key) ?? "[]");
    return Array.isArray(parsed)
      ? parsed.filter((value): value is string => typeof value === "string")
      : [];
  } catch {
    return [];
  }
}

function RecordObjectNavigation({
  app,
  pathname,
  canAdmin,
}: {
  app: RecordAppNav;
  pathname: string;
  canAdmin: boolean;
}) {
  const appHref = `/a/${app.appId}`;
  const pathObject = pathname.split("/")[3];
  const activeObjectApiName = app.objects.some(
    (object) => object.apiName === pathObject,
  )
    ? pathObject
    : null;
  const favoritesKey = `openneko.record-favorites.${app.appId}`;
  const recentsKey = `openneko.record-recents.${app.appId}`;
  const [query, setQuery] = useState("");
  const [favorites, setFavorites] = useState<string[]>([]);
  const [recents, setRecents] = useState<string[]>([]);
  const [expanded, setExpanded] = useState(false);

  useEffect(() => {
    const id = window.setTimeout(() => {
      setFavorites(storedObjectIds(favoritesKey));
    }, 0);
    return () => window.clearTimeout(id);
  }, [favoritesKey]);

  useEffect(() => {
    const id = window.setTimeout(() => {
      const stored = storedObjectIds(recentsKey);
      const next = activeObjectApiName
        ? [
            activeObjectApiName,
            ...stored.filter((value) => value !== activeObjectApiName),
          ].slice(0, 8)
        : stored;
      setRecents(next);
      window.localStorage.setItem(recentsKey, JSON.stringify(next));
    }, 0);
    return () => window.clearTimeout(id);
  }, [activeObjectApiName, recentsKey]);

  const navigation = useMemo(
    () =>
      buildRecordNavSections({
        objects: app.objects,
        favorites,
        recents,
        activeObjectApiName: RECORDS_VISUAL_TEST ? null : activeObjectApiName,
        query,
        expanded,
      }),
    [activeObjectApiName, app.objects, expanded, favorites, query, recents],
  );

  function toggleFavorite(apiName: string) {
    setFavorites((current) => {
      const next = current.includes(apiName)
        ? current.filter((value) => value !== apiName)
        : [...current, apiName];
      window.localStorage.setItem(favoritesKey, JSON.stringify(next));
      return next;
    });
  }

  return (
    <div className="app-rail-record-browser">
      <label className="app-rail-record-search">
        <Search aria-hidden="true" strokeWidth={2} />
        <span className="sr-only">Search {app.label} objects</span>
        <Input
          type="search"
          value={query}
          onChange={(event) => {
            setQuery(event.target.value);
            setExpanded(false);
          }}
          placeholder="Find an object"
        />
      </label>
      <div
        className="app-rail-record-objects"
        aria-label={`${app.label} objects`}
      >
        {navigation.sections.map((section) => (
          <section className="app-rail-record-section" key={section.id}>
            <div className="app-rail-record-section-label">{section.label}</div>
            {section.objects.map((object) => {
              const href = `${appHref}/${object.apiName}`;
              const active = isActive(pathname, href);
              const favorite = favorites.includes(object.apiName);
              return (
                <div
                  className="app-rail-record-object-wrap"
                  key={object.apiName}
                >
                  <Link
                    href={href}
                    className={`app-rail-record-object${active ? " is-active" : ""}`}
                    aria-current={active ? "page" : undefined}
                    title={object.pluralLabel}
                  >
                    <Table2 aria-hidden="true" strokeWidth={1.8} />
                    <span className="app-rail-record-object-label">
                      {object.pluralLabel}
                    </span>
                    {object.custom && (
                      <span className="app-rail-record-custom">__c</span>
                    )}
                    {object.recordCount !== null && (
                      <span className="app-rail-record-count font-mono">
                        {object.recordCount}
                      </span>
                    )}
                  </Link>
                  <Button
                    variant="ghost"
                    type="button"
                    className={`app-rail-record-favorite${favorite ? " is-favorite" : ""}`}
                    aria-label={`${favorite ? "Remove" : "Add"} ${object.pluralLabel} ${favorite ? "from" : "to"} favorites`}
                    aria-pressed={favorite}
                    onClick={() => toggleFavorite(object.apiName)}
                  >
                    <Star
                      aria-hidden="true"
                      fill={favorite ? "currentColor" : "none"}
                    />
                  </Button>
                </div>
              );
            })}
          </section>
        ))}
        {navigation.sections.length === 0 && (
          <div className="app-rail-record-empty">
            No objects match “{query}”.
          </div>
        )}
        {navigation.hiddenCount > 0 && (
          <Button
            variant="ghost"
            type="button"
            className="app-rail-record-more"
            onClick={() => setExpanded(true)}
          >
            Show {navigation.hiddenCount} more
          </Button>
        )}
        {expanded &&
          navigation.hiddenCount === 0 &&
          app.objects.length > 12 && (
            <Button
              variant="ghost"
              type="button"
              className="app-rail-record-more"
              onClick={() => setExpanded(false)}
            >
              Show less
            </Button>
          )}
        {canAdmin && (
          <section className="app-rail-record-section is-admin">
            <div className="app-rail-record-section-label">Manage</div>
            <div className="app-rail-record-object-wrap">
              <Link
                href={`${appHref}/admin`}
                className={`app-rail-record-object${isActive(pathname, `${appHref}/admin`) ? " is-active" : ""}`}
                aria-current={
                  isActive(pathname, `${appHref}/admin`) ? "page" : undefined
                }
                title={`${app.label} admin`}
              >
                <Settings2 aria-hidden="true" strokeWidth={1.8} />
                <span className="app-rail-record-object-label">Admin</span>
              </Link>
            </div>
          </section>
        )}
      </div>
    </div>
  );
}

function initials(user: SessionUser | null) {
  const source = user?.name || user?.email || "A";
  return (
    source
      .split(/[\s@._-]+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((part) => part[0]?.toUpperCase())
      .join("") || "A"
  );
}

function RailLink({
  item,
  active,
  pending,
}: {
  item: NavDestination;
  active: boolean;
  pending: number;
}) {
  const Icon = item.icon;
  const isActions = item.href === "/actions";

  return (
    <div className="app-rail-link-wrap">
      <Link
        href={item.href}
        className={`app-rail-link${active ? " is-active" : ""}`}
        aria-current={active ? "page" : undefined}
        title={item.label}
      >
        <Icon aria-hidden="true" strokeWidth={2} />
        <span className="app-rail-label">{item.label}</span>
        <span className="app-rail-short">{item.shortLabel}</span>
        {isActions && pending > 0 && (
          <span
            className="app-rail-badge font-mono"
            aria-label={`${pending} pending`}
          >
            {pending}
          </span>
        )}
      </Link>
    </div>
  );
}

export default function AppRail() {
  const pathname = usePathname() ?? "/";
  const router = useRouter();
  const hidden = hideAppChrome(pathname);
  const pending = useApprovalsCount();
  const navRef = useRef<HTMLElement>(null);
  const [user, setUser] = useState<SessionUser | null>(
    RECORDS_VISUAL_TEST
      ? { email: "kavya@example.com", name: "Kavya M." }
      : null,
  );
  const [sessionMode, setSessionMode] = useState<
    "loading" | "solo" | "admin" | "member"
  >(RECORDS_VISUAL_TEST ? "member" : "loading");
  const [latestVersion, setLatestVersion] = useState<string | null>(null);
  const [recordApps, setRecordApps] = useState<RecordAppNav[]>(
    RECORDS_VISUAL_TEST
      ? RECORDS_VISUAL_NAV_APPS.map((app) => ({
          ...app,
          purpose: app.purpose,
          objects: app.objects.map((object) => ({ ...object })),
        }))
      : [],
  );
  const [recordAppsUnavailable, setRecordAppsUnavailable] = useState(false);

  const grouped = useMemo(() => {
    const visible = ALL_NAV.filter(
      (item) =>
        item.href !== "/admin" ||
        (sessionMode !== "loading" && sessionMode !== "member"),
    );
    return {
      primary: visible.filter((n) => n.group === "primary"),
      knowledge: visible.filter((n) => n.group === "knowledge"),
      workspace: visible.filter((n) => n.group === "workspace"),
    };
  }, [sessionMode]);

  useEffect(() => {
    if (hidden || RECORDS_VISUAL_TEST) return;
    let cancelled = false;
    const load = async () => {
      try {
        const res = await fetch("/api/auth/session", { cache: "no-store" });
        if (cancelled || !res.ok) return;
        const data = (await res.json()) as {
          user: { id: string; email: string; name: string | null } | null;
          authEnabled?: boolean;
          role: "admin" | "member" | null;
        };
        if (data.user) {
          setUser({ email: data.user.email, name: data.user.name });
          setSessionMode(data.authEnabled === false ? "solo" : data.role === "member" ? "member" : "admin");
        } else {
          setSessionMode("solo");
        }
      } catch {
        // Keep admin-only navigation hidden when identity cannot be resolved.
      }
    };
    void load();
    // Re-check periodically: the identity fetch is one-shot otherwise, and a
    // worker blip at page load would leave the profile/signout block missing
    // until a full reload.
    const id = setInterval(load, 30_000);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [hidden]);

  useEffect(() => {
    const frame = window.requestAnimationFrame(() => {
      navRef.current
        ?.querySelector<HTMLElement>('[aria-current="page"]')
        ?.scrollIntoView({ block: "nearest", inline: "nearest" });
    });
    return () => window.cancelAnimationFrame(frame);
  }, [pathname, recordApps.length, sessionMode]);

  useEffect(() => {
    if (hidden || RECORDS_VISUAL_TEST) return;
    let cancelled = false;
    const load = async () => {
      try {
        const response = await fetch("/api/a/apps", { cache: "no-store" });
        if (cancelled) return;
        if (!response.ok) {
          setRecordAppsUnavailable(response.status >= 500);
          return;
        }
        const data = (await response.json()) as { apps?: RecordAppNav[] };
        setRecordApps(Array.isArray(data.apps) ? data.apps : []);
        setRecordAppsUnavailable(false);
      } catch {
        if (!cancelled) setRecordAppsUnavailable(true);
      }
    };
    void load();
    const id = setInterval(load, 60_000);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [hidden]);

  useEffect(() => {
    if (hidden || RECORDS_VISUAL_TEST) return;
    let cancelled = false;
    const check = async () => {
      try {
        const res = await fetch("/api/version", { cache: "no-store" });
        if (!res.ok) return;
        const data = (await res.json()) as { version?: string };
        if (!cancelled && typeof data.version === "string") {
          setLatestVersion(data.version);
        }
      } catch {
        // Best effort.
      }
    };
    void check();
    const id = setInterval(check, VERSION_POLL_MS);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [hidden]);

  async function handleSignOut() {
    try {
      await fetch("/api/auth/logout", { method: "POST" });
    } finally {
      window.location.href = "/signin";
    }
  }

  if (hidden) return null;

  const updateAvailable =
    latestVersion !== null && latestVersion !== APP_VERSION;
  const activeRecordApp =
    recordApps.find((app) => isActive(pathname, `/a/${app.appId}`)) ?? null;

  return (
    <aside className="app-rail-wrap" aria-label="Primary navigation">
      <div className="app-rail-brand">
        <a
          className="app-rail-brand-link"
          href="https://openneko.app"
          target="_blank"
          rel="noopener noreferrer"
          aria-label="OpenNeko — open website in a new tab"
        >
          <Image
            className="app-rail-logo"
            src="/cat.png"
            alt=""
            width={26}
            height={27}
          />
          <span className="app-rail-name">OpenNeko</span>
        </a>
        {RECORDS_VISUAL_TEST ? null : updateAvailable ? (
          <Button
            variant="ghost"
            type="button"
            className="app-rail-version is-update"
            onClick={() => window.location.reload()}
            title={`v${latestVersion} available; reload`}
          >
            v{latestVersion}
          </Button>
        ) : (
          <span className="app-rail-version">{APP_VERSION}</span>
        )}
      </div>

      <nav ref={navRef} className="app-rail-nav">
        <div className="app-rail-group">
          {grouped.primary.map((item) => (
            <RailLink
              key={item.href}
              item={item}
              active={isActive(pathname, item.href)}
              pending={pending}
            />
          ))}
        </div>

        <>
          <div className="app-rail-heading app-rail-records-heading">Apps</div>
          <div className="app-rail-group">
            <div className="app-rail-link-wrap">
              <Link
                href="/apps"
                className={`app-rail-link${isActive(pathname, "/apps") ? " is-active" : ""}`}
                aria-current={isActive(pathname, "/apps") ? "page" : undefined}
                title="All apps"
              >
                <LayoutGrid aria-hidden="true" strokeWidth={2} />
                <span className="app-rail-label">All apps</span>
                <span className="app-rail-short">Apps</span>
              </Link>
            </div>
          </div>
          {recordAppsUnavailable ? (
            <span className="app-rail-record-status">
              Temporarily unavailable
            </span>
          ) : recordApps.length > 0 ? (
            <>
              <label className="app-rail-record-switcher">
                <span className="sr-only">Choose an app</span>
                <Database aria-hidden="true" strokeWidth={2} />
                <NativeSelect
                  value={activeRecordApp?.appId ?? ""}
                  onChange={(event) => {
                    if (event.target.value)
                      router.push(`/a/${event.target.value}`);
                  }}
                >
                  <option value="">Choose an app</option>
                  {recordApps.map((app) => (
                    <option key={app.appId} value={app.appId}>
                      {app.label}
                    </option>
                  ))}
                </NativeSelect>
              </label>
              <div className="app-rail-record-compact-apps">
                {recordApps.map((app) => {
                  const appHref = `/a/${app.appId}`;
                  const appActive = isActive(pathname, appHref);
                  return (
                    <div className="app-rail-record-app" key={app.appId}>
                      <div className="app-rail-group">
                        <div className="app-rail-link-wrap">
                          <Link
                            href={appHref}
                            className={`app-rail-link app-rail-record-link${appActive ? " is-active" : ""}`}
                            aria-current={
                              pathname === appHref ? "page" : undefined
                            }
                            title={app.label}
                          >
                            <Database aria-hidden="true" strokeWidth={2} />
                            <span className="app-rail-label">{app.label}</span>
                            <span className="app-rail-short">
                              {app.label.slice(0, 7)}
                            </span>
                          </Link>
                        </div>
                      </div>
                    </div>
                  );
                })}
              </div>
              {activeRecordApp && (
                <RecordObjectNavigation
                  key={activeRecordApp.appId}
                  app={activeRecordApp}
                  pathname={pathname}
                  canAdmin={
                    sessionMode !== "member" && sessionMode !== "loading"
                  }
                />
              )}
            </>
          ) : null}
        </>

        {!RECORDS_VISUAL_TEST && (
          <>
            <div className="app-rail-heading">Knowledge</div>
            <div className="app-rail-group">
              {grouped.knowledge.map((item) => (
                <RailLink
                  key={item.href}
                  item={item}
                  active={isActive(pathname, item.href)}
                  pending={pending}
                />
              ))}
            </div>

            <div className="app-rail-heading">Workspace</div>
            <div className="app-rail-group">
              {grouped.workspace.map((item) => (
                <RailLink
                  key={item.href}
                  item={item}
                  active={isActive(pathname, item.href)}
                  pending={pending}
                />
              ))}
            </div>
          </>
        )}
      </nav>

      <div className="app-rail-foot">
        {!RECORDS_VISUAL_TEST && (
          <div className="app-rail-density">
            <span className="app-rail-foot-label">Density</span>
            <DensityToggle />
          </div>
        )}
        {user ? (
          <div className="app-rail-user">
            <Link
              href="/onboarding"
              className="app-rail-user-profile"
              title="Profile"
            >
              <span className="app-rail-avatar" aria-hidden="true">
                {initials(user)}
              </span>
              <span className="app-rail-user-copy">
                <span className="app-rail-user-name">
                  {user.name || user.email}
                </span>
                <span className="app-rail-user-email">
                  {user.name ? user.email : "Profile"}
                </span>
              </span>
            </Link>
            {sessionMode !== "solo" && <Button
              variant="ghost"
              type="button"
              className="app-rail-signout"
              onClick={handleSignOut}
              aria-label={`Sign out ${user.email}`}
              title={`Sign out ${user.email}`}
            >
              <LogOut aria-hidden="true" strokeWidth={2} />
            </Button>}
          </div>
        ) : null}
      </div>
    </aside>
  );
}
