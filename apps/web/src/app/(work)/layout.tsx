"use client";

import { useEffect } from "react";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import SectionNav from "@/components/SectionNav";
import WorkSidebar from "./work/WorkSidebar";
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

  return (
    <WorkShellProvider>
      <div className="root">
        <AppHeader>
          <SectionNav current="work" />
        </AppHeader>

        <div className="work-layout">
          <WorkSidebar />
          <section className="work-panel">{children}</section>
        </div>
      </div>
      <CreatorCredit />
    </WorkShellProvider>
  );
}
