"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import AppHeader from "@/components/AppHeader";
import PageHeading from "@/components/PageHeading";
import SectionNav from "@/components/SectionNav";
import { Button } from "@/components/ui/button";
import AdminLoad from "@/app/admin/AdminLoad";
import { WorkflowApiAccessPanel } from "@/app/(work)/workflows/WorkflowApiAccessPanel";
import PublicUrlForm from "./PublicUrlForm";
import WorkflowOrgLimitsForm from "./WorkflowOrgLimitsForm";

type WorkflowItem = { id: string; name: string };
type PublicUrl = Parameters<typeof PublicUrlForm>[0]["initial"];
type Limits = { rollingTokenBudget: number | null; rollingCostMicrosBudget: number | null };

export default function WorkflowSettingsPage() {
  return (
    <AdminLoad
      title="Workflow API"
      paths={["/api/v1/workflows", "/api/v1/admin/workflow-api-limits", "/api/v1/admin/public-url"]}
    >
      {([workflows, limits, publicUrl]) => (
        <WorkflowSettings
          workflows={(workflows as { workflows: WorkflowItem[] }).workflows}
          limits={(limits as { limits: Limits }).limits}
          publicUrl={publicUrl as PublicUrl}
        />
      )}
    </AdminLoad>
  );
}

function WorkflowSettings({
  workflows,
  limits,
  publicUrl,
}: {
  workflows: WorkflowItem[];
  limits: Limits;
  publicUrl: PublicUrl;
}) {
  const selected = useSearchParams().get("workflow");
  const workflow = workflows.find((item) => item.id === selected) ?? workflows[0];

  return (
    <div className="root">
      <AppHeader back={{ href: "/admin/settings", label: "Settings" }}>
        <SectionNav current="admin" />
      </AppHeader>
      <PageHeading
        title="Workflow API"
        description="Set the public URL, the organization budget, and each workflow's API execution limits. Runtime also sets the agent's time allowance for API runs."
      />
      <PublicUrlForm initial={publicUrl} />
      <WorkflowOrgLimitsForm initial={{
        rollingTokenBudget: limits.rollingTokenBudget ?? 1_000_000,
        rollingCostMicrosBudget: limits.rollingCostMicrosBudget ?? 50_000_000,
      }} />
      <section className="settings-card mt-6 grid gap-4">
        <div>
          <h2 className="settings-card-title">Per-workflow limits</h2>
          <p className="settings-card-copy">Select a workflow to set its runtime, call, token, and artifact limits.</p>
        </div>
        {workflows.length === 0 ? (
          <p className="text-text2">Create a workflow before setting its API limits.</p>
        ) : (
          <nav aria-label="Choose workflow" className="flex flex-wrap gap-2">
            {workflows.map((item) => (
              <Button
                key={item.id}
                asChild
                size="sm"
                variant={item.id === workflow?.id ? "primary" : "secondary"}
              >
                <Link
                  href={`/admin/settings/workflows?workflow=${item.id}`}
                  aria-current={item.id === workflow?.id ? "page" : undefined}
                >
                  {item.name}
                </Link>
              </Button>
            ))}
          </nav>
        )}
        {workflow ? (
          <div className="settings-workflow-api">
            <WorkflowApiAccessPanel key={workflow.id} workflowId={workflow.id} />
          </div>
        ) : null}
      </section>
    </div>
  );
}
