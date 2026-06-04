"use client";

import { useEffect } from "react";
import { usePathname } from "next/navigation";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import SectionNav from "@/components/SectionNav";
import WorkSidebar from "./work/WorkSidebar";
import WorkContextRail from "./work/WorkContextRail";
import { WorkShellProvider } from "./work-shell-context";

export default function WorkShellLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  // The CreatorCredit pill repositions to the bottom-left gutter under
  // the sidebar on desktop instead of overlapping the floating composer.
  // The marker lives on <body> so a CSS-only rule can flip placement.
  useEffect(() => {
    document.body.classList.add("work-shell");
    return () => document.body.classList.remove("work-shell");
  }, []);

  // The context rail (3rd pane) belongs only to the Ask thread pages, not the
  // other surfaces (workflows/skills/memory) that share this shell.
  const pathname = usePathname();
  const showRail = pathname === "/work" || pathname?.startsWith("/work/");

  return (
    <WorkShellProvider>
      <div className="root">
        <AppHeader>
          <SectionNav current="work" />
        </AppHeader>

        <div className={`work-layout${showRail ? " has-rail" : ""}`}>
          <WorkSidebar />
          <section className="min-h-[72vh] flex flex-col gap-[18px]">{children}</section>
          {showRail && <WorkContextRail />}
        </div>
      </div>
      <CreatorCredit />
    </WorkShellProvider>
  );
}
