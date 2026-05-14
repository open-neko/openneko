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
