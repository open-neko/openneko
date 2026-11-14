import { Suspense, useEffect, useMemo, useState, type ComponentType, type ReactNode } from "react";
import { Navigate, Outlet, useLocation, useParams, type RouteObject } from "react-router";
import RootLayout, { useIdentity } from "./RootLayout";
import RouteError, { NotFound } from "./RouteError";
import { loadIdentity } from "./identity";
import { serverPages } from "./server-pages";

function page(load: () => Promise<{ default: ComponentType }>): Pick<RouteObject, "lazy"> {
  return { lazy: async () => ({ Component: (await load()).default }) };
}

function layout(load: () => Promise<{ default: ComponentType<{ children: ReactNode }> }>): Pick<RouteObject, "lazy"> {
  return {
    lazy: async () => {
      const Layout = (await load()).default;
      return { Component: () => <Layout><Outlet /></Layout> };
    },
  };
}

function Empty() {
  return null;
}

function chatLayout(): Pick<RouteObject, "lazy"> {
  return {
    lazy: async () => {
      const ChatShell = (await import("@/components/chat/ChatShell")).default;
      return { Component: () => <ChatShell identity={useIdentity()}><Outlet /></ChatShell> };
    },
  };
}

function BotInfoRoute({ BotInfo }: { BotInfo: ComponentType<{ botId: string }> }) {
  const { botId = "" } = useParams();
  return <BotInfo botId={botId} />;
}

function SkillDetailRoute({ Detail }: { Detail: ComponentType<{ params: Promise<{ name: string }> }> }) {
  const { name = "" } = useParams();
  const params = useMemo(() => Promise.resolve({ name }), [name]);
  return <Detail params={params} />;
}

function WorkflowRedirect() {
  const { workflowId = "" } = useParams();
  return <Navigate to={`/workflows?id=${encodeURIComponent(workflowId)}`} replace />;
}

function PolicyRedirect() {
  const { policyId = "" } = useParams();
  return <Navigate to={`/admin/rules/${encodeURIComponent(policyId)}`} replace />;
}

const HARD_LOAD_KEY = "openneko-hard-load";

function ServerPage() {
  const location = useLocation();
  const target = `${location.pathname}${location.search}`;
  const [loop] = useState(() => sessionStorage.getItem(HARD_LOAD_KEY) === target);
  useEffect(() => {
    if (loop) {
      sessionStorage.removeItem(HARD_LOAD_KEY);
      return;
    }
    sessionStorage.setItem(HARD_LOAD_KEY, target);
    window.location.assign(`${target}${location.hash}`);
  }, [loop, target, location.hash]);
  return loop ? <NotFound /> : null;
}

export const redirects: Record<string, string> = {
  "/": "/chat",
  "/settings": "/admin/settings",
  "/settings/agent": "/admin/settings/agent",
  "/settings/data": "/admin/settings/data",
  "/settings/research": "/admin/settings/research",
  "/settings/security": "/admin/settings/security",
  "/settings/rules": "/admin/rules",
  "/approvals": "/actions",
  "/admin/graphjin": "/admin/settings/graphjin",
  "/admin/settings/rules": "/admin/rules",
};


const workShell: RouteObject = {
  ...layout(() => import("@/app/(work)/layout")),
  children: [
    {
      path: "chat",
      ...chatLayout(),
      children: [
        { index: true, ...page(() => import("@/components/chat/ChatHome")) },
        {
          path: "t",
          ...layout(() => import("@/app/(work)/chat/t/layout")),
          children: [
            { index: true, Component: Empty },
            { path: ":threadId", Component: Empty },
          ],
        },
        { path: "bots/new", ...page(() => import("@/components/chat/CreateBot")) },
        {
          path: "bots/:botId",
          lazy: async () => {
            const BotInfo = (await import("@/components/chat/BotInfo")).default;
            return { Component: () => <BotInfoRoute BotInfo={BotInfo} /> };
          },
        },
      ],
    },
    {
      path: "work",
      ...layout(() => import("@/app/(work)/work/layout")),
      children: [
        { index: true, Component: Empty },
        { path: ":threadId", Component: Empty },
      ],
    },
    { path: "workflows", ...page(() => import("@/app/(work)/workflows/page")) },
    { path: "library", ...page(() => import("@/app/(work)/library/page")) },
    { path: "memory", ...page(() => import("@/app/(work)/memory/page")) },
    { path: "skills", ...page(() => import("@/app/(work)/skills/page")) },
    { path: "profile", ...page(() => import("@/app/(work)/profile/page")) },
  ],
};

export const routes: RouteObject[] = [
  {
    id: "root",
    path: "/",
    loader: loadIdentity,
    Component: RootLayout,
    errorElement: <RouteError />,
    HydrateFallback: Empty,
    shouldRevalidate: ({ currentUrl, nextUrl, defaultShouldRevalidate }) =>
      currentUrl.pathname === nextUrl.pathname && defaultShouldRevalidate,
    children: [
      {
        errorElement: <RouteError />,
        children: [
          ...Object.entries(redirects).map(([from, to]) =>
            from === "/" ? { index: true, element: <Navigate to={to} replace /> } : { path: from.slice(1), element: <Navigate to={to} replace /> },
          ),
          { path: "workflows/:workflowId", Component: WorkflowRedirect },
          { path: "admin/settings/rules/:policyId", Component: PolicyRedirect },
          { path: "settings/rules/:policyId", Component: PolicyRedirect },
          workShell,
          { path: "actions", ...page(() => import("@/app/actions/page")) },
          { path: "actions/:actionRequestId", ...page(() => import("@/app/actions/[actionRequestId]/page")) },
          { path: "business-profile", ...page(() => import("@/app/business-profile/page")) },
          { path: "runs", ...page(() => import("@/app/runs/page")) },
          { path: "runs/:workflowRunId", ...page(() => import("@/app/runs/[workflowRunId]/page")) },
          { path: "signin", ...page(() => import("@/app/signin/page")) },
          { path: "admin/rules", ...page(() => import("@/app/admin/rules/page")) },
          { path: "admin/rules/:policyId", ...page(() => import("@/app/admin/rules/[policyId]/page")) },
          { path: "admin/settings/channels", ...page(() => import("@/app/admin/settings/channels/page")) },
          { path: "admin/settings/packs", ...page(() => import("@/app/admin/settings/packs/page")) },
          { path: "admin/settings/signin", ...page(() => import("@/app/admin/settings/signin/page")) },
          { path: "admin/settings/agent", ...page(() => import("@/app/admin/settings/agent/page")) },
          { path: "admin/settings/data", ...page(() => import("@/app/admin/settings/data/page")) },
          { path: "admin/settings/models", ...page(() => import("@/app/admin/settings/models/page")) },
          { path: "admin/settings/research", ...page(() => import("@/app/admin/settings/research/page")) },
          { path: "admin/settings/security", ...page(() => import("@/app/admin/settings/security/page")) },
          { path: "admin/settings/spend", ...page(() => import("@/app/admin/settings/spend/page")) },
          { path: "admin/settings/sso", ...page(() => import("@/app/admin/settings/sso/page")) },
          { path: "admin", ...page(() => import("@/app/admin/page")) },
          { path: "admin/settings", ...page(() => import("@/app/admin/settings/page")) },
          { path: "admin/settings/workflows", ...page(() => import("@/app/admin/settings/workflows/page")) },
          {
            path: "skills/:name",
            lazy: async () => {
              const Detail = (await import("@/app/skills/[name]/page")).default;
              return { Component: () => <SkillDetailRoute Detail={Detail} /> };
            },
          },
          ...serverPages.map((path) => ({ path: path.slice(1), Component: ServerPage })),
          ...(import.meta.env.DEV
            ? [{
                path: "dev/answer-preview",
                lazy: async () => {
                  const Preview = (await import("@/app/dev/answer-preview/preview-client")).default;
                  return { Component: () => <SuspendedPreview Preview={Preview} /> };
                },
              }]
            : []),
          { path: "*", Component: NotFound },
        ],
      },
    ],
  },
];

function SuspendedPreview({ Preview }: { Preview: ComponentType<{ fixture: string }> }) {
  const fixture = new URLSearchParams(useLocation().search).get("f") ?? "report";
  return <Suspense fallback={null}><Preview fixture={fixture} /></Suspense>;
}
