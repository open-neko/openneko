import { Outlet, ScrollRestoration, useRouteLoaderData } from "react-router";
import { DensityProvider } from "@/components/DensityProvider";
import AppRail, { type RailIdentity } from "@/components/AppRail";
import CommandDock from "@/components/CommandDock";
import CommandBar from "@/components/CommandBar";
import AppToaster from "@/components/AppToaster";

export function useIdentity(): RailIdentity | undefined {
  return useRouteLoaderData("root") as RailIdentity | undefined;
}

export default function RootLayout() {
  const identity = useIdentity();
  return (
    <>
      <a href="#main-content" className="skip-to-main">
        Skip to main content
      </a>
      <DensityProvider>
        <AppRail initial={identity} />
        <div id="main-content">
          <Outlet />
        </div>
        <CommandDock />
        <CommandBar />
      </DensityProvider>
      <AppToaster />
      <ScrollRestoration />
    </>
  );
}
